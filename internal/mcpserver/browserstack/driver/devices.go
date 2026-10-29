package driver

import (
	"fmt"
	"strings"
)

// Kind is the sort of device: ios, android or desktop.
type Kind string

// The device kinds.
const (
	IOS     Kind = "ios"
	Android Kind = "android"
	Desktop Kind = "desktop"
)

// Device is one device of the matrix.
type Device struct {
	ID    string
	Label string
	Kind  Kind
	// BrowserName is the W3C browserName; Options are BrowserStack's
	// bstack:options without the session fields.
	BrowserName string
	Options     map[string]string
	// Extra marks a device outside the matrix: run only by flows that name it.
	Extra bool
}

// Devices are the matrix columns, in report order, the extra ones last.
var Devices = []Device{
	{ID: "iphone", Label: "iPhone 15 · Safari", Kind: IOS, BrowserName: "safari", Options: map[string]string{"deviceName": "iPhone 15", "osVersion": "27", "realMobile": "true"}},
	{ID: "iphone-old", Label: "iPhone 13 · iOS 16", Kind: IOS, BrowserName: "safari", Options: map[string]string{"deviceName": "iPhone 13", "osVersion": "16", "realMobile": "true"}},
	{ID: "ipad", Label: "iPad 10th · Safari", Kind: IOS, BrowserName: "safari", Options: map[string]string{"deviceName": "iPad 10th", "osVersion": "27", "realMobile": "true"}},
	{ID: "pixel", Label: "Pixel 9 · Chrome", Kind: Android, BrowserName: "chrome", Options: map[string]string{"deviceName": "Google Pixel 9", "osVersion": "16.0", "realMobile": "true"}},
	{ID: "moto", Label: "Moto G71 · Chrome", Kind: Android, BrowserName: "chrome", Options: map[string]string{"deviceName": "Motorola Moto G71 5G", "osVersion": "11.0", "realMobile": "true"}},
	{ID: "samsung", Label: "Galaxy S25 · Samsung Internet", Kind: Android, BrowserName: "samsung", Options: map[string]string{"deviceName": "Samsung Galaxy S25", "osVersion": "15.0", "realMobile": "true"}},
	{ID: "win-chrome", Label: "Windows · Chrome", Kind: Desktop, BrowserName: "chrome", Options: map[string]string{"os": "Windows", "osVersion": "11", "resolution": DesktopResolution}},
	{ID: "win-firefox", Label: "Windows · Firefox", Kind: Desktop, BrowserName: "firefox", Options: map[string]string{"os": "Windows", "osVersion": "11", "resolution": DesktopResolution}},
	{ID: "mac-safari", Label: "macOS · Safari", Kind: Desktop, BrowserName: "safari", Options: map[string]string{"os": "OS X", "osVersion": "Tahoe", "resolution": DesktopResolution}},
	{ID: "win-edge", Label: "Windows · Edge", Kind: Desktop, BrowserName: "edge", Options: map[string]string{"os": "Windows", "osVersion": "11", "resolution": DesktopResolution}, Extra: true},
	{ID: "iphone-se-ios15", Label: "iPhone SE · iOS 15", Kind: IOS, BrowserName: "safari", Options: map[string]string{"deviceName": "iPhone SE 2022", "osVersion": "15", "realMobile": "true"}, Extra: true},
}

// DeviceByID returns the device with the id.
func DeviceByID(id string) (Device, error) {
	ids := make([]string, 0, len(Devices))
	for _, d := range Devices {
		if d.ID == id {
			return d, nil
		}
		ids = append(ids, d.ID)
	}
	return Device{}, fmt.Errorf("unknown device %s (known: %s)", id, strings.Join(ids, ", "))
}
