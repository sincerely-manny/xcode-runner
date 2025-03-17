package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/manifoldco/promptui"
)

type Device struct {
	Name  string `json:"name"`
	UDID  string `json:"udid"`
	Avail bool   `json:"isAvailable"`
}

type DeviceList struct {
	Devices map[string][]Device `json:"devices"`
}

type BuildSettings struct {
	BUILT_PRODUCTS_DIR        string `json:"BUILT_PRODUCTS_DIR"`
	CONTENTS_FOLDER_PATH      string `json:"CONTENTS_FOLDER_PATH"`
	PRODUCT_BUNDLE_IDENTIFIER string `json:"PRODUCT_BUNDLE_IDENTIFIER"`
}

func RunShellCommand(command string, args ...string) (string, error) {
	cmd := exec.Command(command, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("error running command: %s\n%s", err, out.String())
	}
	return out.String(), nil
}

func GetSchemes() ([]string, error) {
	output, err := RunShellCommand("xcodebuild", "-list")
	if err != nil {
		return nil, err
	}

	var schemes []string
	inSchemes := false
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if inSchemes && line != "" {
			schemes = append(schemes, line)
		}
		if strings.Contains(line, "Schemes:") {
			inSchemes = true
		}
	}
	if len(schemes) == 0 {
		return nil, fmt.Errorf("no schemes found")
	}
	return schemes, nil
}

func GetDevices() (map[string]string, error) {
	cmd := exec.Command("xcrun", "xctrace", "list", "devices")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	devices := make(map[string]string)
	re := regexp.MustCompile(`^(.+) \(([A-F0-9]{8}-[A-F0-9]{4}-[A-F0-9]{4}-[A-F0-9]{4}-[A-F0-9]{12}|[0-9]{8}-[0-9]{16})\)$`)

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	currentSection := ""

	for scanner.Scan() {
		line := scanner.Text()

		// Check section headers to determine current section
		if strings.HasPrefix(line, "== ") {
			currentSection = line
			continue
		}

		// Skip empty lines and offline devices section
		if line == "" || currentSection == "== Devices Offline ==" {
			continue
		}

		// Process only devices from the "Devices" and "Simulators" sections
		if currentSection == "== Devices ==" || currentSection == "== Simulators ==" {
			// Extract the last part between parentheses (the UDID)
			lastParenIndex := strings.LastIndex(line, "(")
			if lastParenIndex == -1 {
				continue
			}

			udidPart := line[lastParenIndex+1:]
			udid := strings.TrimSuffix(udidPart, ")")

			// Clean up the device name by removing the UDID part and any version info
			deviceName := line[:lastParenIndex-1]

			// If there are nested parentheses for version info, clean that up too
			versionParenIndex := strings.LastIndex(deviceName, "(")
			if versionParenIndex != -1 {
				deviceName = strings.TrimSpace(deviceName[:versionParenIndex])
			}

			// Check if the UDID matches the expected format
			if matched := re.MatchString(line); matched || (len(udid) > 0 && (strings.Contains(udid, "-") || strings.ContainsAny(udid, "0123456789ABCDEF"))) {
				devices[deviceName] = udid
			}
		}
	}

	if len(devices) == 0 {
		return nil, fmt.Errorf("no available simulators found")
	}

	return devices, nil
}

func PromptUser(label string, items []string) (string, error) {
	prompt := promptui.Select{
		Label: label,
		Items: items,
	}
	_, result, err := prompt.Run()
	if err != nil {
		return "", err
	}
	return result, nil
}

func main() {
	fmt.Println("🚀 Xcode Runner CLI")

	projectPath, err := detectXcodeProject()
	if err != nil {
		fmt.Println("❌ Error:", err)
		return
	}
	fmt.Println("📂 Detected Xcode project:", projectPath)

	schemes, err := GetSchemes()
	if err != nil {
		fmt.Println("❌ Error fetching schemes:", err)
		return
	}
	selectedScheme := schemes[0]
	// selectedScheme, err := PromptUser("Select a Scheme", schemes)
	// if err != nil {
	// 	fmt.Println("❌ Error selecting scheme:", err)
	// 	return
	// }

	devices, err := GetDevices()
	if err != nil {
		fmt.Println("❌ Error fetching devices:", err)
		return
	}

	deviceNames := make([]string, 0, len(devices))
	for name := range devices {
		deviceNames = append(deviceNames, name)
	}

	selectedDevice, err := PromptUser("Select a Device", deviceNames)
	if err != nil {
		fmt.Println("❌ Error selecting device:", err)
		return
	}
	deviceUDID, found := devices[selectedDevice]
	if !found {
		fmt.Println("❌ Error: Could not find UDID for selected device.")
		return
	}

	fmt.Printf("\n🔨 Building %s for %s (%s)...\n", selectedScheme, selectedDevice, deviceUDID)

	appPath, bundleIdentifier, err := GetBuildSettings(selectedScheme, deviceUDID)
	if err != nil {
		fmt.Println("❌ Error getting build settings:", err)
		return
	}
	if appPath == "" || bundleIdentifier == "" {
		fmt.Println("❌ Error: Could not find app path or bundle identifier.")
		return
	}

	isSim := strings.Contains(appPath, "simulator")

	buildCmd := exec.Command("xcodebuild",
		"-scheme", selectedScheme,
		"-destination",
		"id="+deviceUDID,
		"-configuration", "Debug",
		"build")
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	err = buildCmd.Run()
	if err != nil {
		fmt.Println("❌ Build failed!")
		return
	}

	if isSim {
		fmt.Println("\n📲 Installing & Launching App on Simulator...")
		exec.Command("xcrun", "simctl", "bootstatus", deviceUDID, "-b").Run()
		exec.Command("xcrun", "simctl", "install", deviceUDID, appPath).Run()
		exec.Command("xcrun", "simctl", "launch", deviceUDID, bundleIdentifier).Run()
	} else {
		fmt.Println("\n🔗 Deploying to Physical Device...")
		// _, err := exec.LookPath("ios-deploy")
		// if err != nil {
		// 	fmt.Println("❌ ios-deploy not found. Install it with: brew install ios-deploy")
		// 	return
		// }
		// exec.Command("ios-deploy", "--bundle", appPath, "--id", deviceUDID, "--debug").Run()
		exec.Command("xcrun", "devicectl", "device", "install", "app", "--device", deviceUDID, "--bundle", appPath).Run()
		exec.Command("xcrun", "devicectl", "device", "process", "launch", "--device", deviceUDID, "--start-stopped", bundleIdentifier).Run()
	}

	fmt.Println("\n✅ Done!")
}

func detectXcodeProject() (string, error) {
	var project string
	var workspace string

	files, err := os.ReadDir(".")
	if err != nil {
		return "", err
	}

	for _, file := range files {
		if file.IsDir() {
			if filepath.Ext(file.Name()) == ".xcworkspace" {
				workspace = file.Name()
			} else if filepath.Ext(file.Name()) == ".xcodeproj" {
				project = file.Name()
			}
		}
	}

	if workspace != "" {
		return workspace, nil
	} else if project != "" {
		return project, nil
	}

	return "", errors.New("no .xcodeproj or .xcworkspace found in the current directory")
}

func GetBuildSettings(selectedScheme, deviceID string) (string, string, error) {
	cmd := exec.Command("xcodebuild", "-scheme", selectedScheme, "-destination", fmt.Sprintf("id=%s", deviceID), "-showBuildSettings", "-json")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", "", err
	}

	var buildSettings []map[string]any
	err = json.Unmarshal(out.Bytes(), &buildSettings)
	if err != nil {
		return "", "", err
	}

	if len(buildSettings) > 0 {
		settings := buildSettings[0]["buildSettings"].(map[string]any)
		builtProductsDir := settings["BUILT_PRODUCTS_DIR"].(string)
		contentsFolderPath := settings["CONTENTS_FOLDER_PATH"].(string)
		bundleIdentifier := settings["PRODUCT_BUNDLE_IDENTIFIER"].(string)

		appPath := fmt.Sprintf("%s/%s", builtProductsDir, contentsFolderPath)
		return appPath, bundleIdentifier, nil
	}

	return "", "", fmt.Errorf("Unable to find the required build settings")
}
