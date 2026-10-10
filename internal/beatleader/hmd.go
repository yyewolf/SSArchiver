package beatleader

import "strconv"

var hmdNames = map[int]string{
	0: "Unknown", 1: "Rift", 2: "Vive", 4: "Vive Pro", 8: "WMR", 16: "Rift S", 32: "Quest",
	33: "Pico Neo 3", 34: "Pico Neo 2", 35: "Vive Pro 2", 36: "Vive Elite", 37: "Miramar",
	38: "Pimax 8K", 39: "Pimax 5K", 40: "Pimax Artisan", 41: "HP Reverb", 42: "Samsung WMR",
	43: "Qiyu Dream", 44: "Disco", 45: "Lenovo Explorer", 46: "Acer WMR", 47: "Vive Focus",
	48: "Arpara", 49: "Dell Visor", 50: "E3", 51: "Vive DVT", 52: "Glasses 2.0", 53: "Hedy",
	54: "Vaporeon", 55: "Huawei VR", 56: "Asus WMR", 57: "CloudXR", 58: "VRidge", 59: "Medion",
	60: "Pico Neo 4", 61: "Quest Pro", 62: "Pimax Crystal", 63: "E4", 64: "Valve Index",
	65: "Controllable", 66: "Bigscreen Beyond", 67: "Nolo Sonic", 68: "Hypereal", 69: "Varjo Aero",
	70: "PS VR2", 71: "MeganeX", 72: "Varjo XR-3", 73: "MeganeX Superlight", 74: "Somnium VR1",
	75: "Steam Frame", 128: "Vive Cosmos", 256: "Quest 2", 512: "Quest 3", 513: "Quest 3S",
}

// HMDName is the display name of a BeatLeader HMD code; unknown codes are
// kept as their number (spec §4.6).
func HMDName(code int) string {
	if n, ok := hmdNames[code]; ok {
		return n
	}
	return strconv.Itoa(code)
}
