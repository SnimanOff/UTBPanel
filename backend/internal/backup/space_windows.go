package backup

import "golang.org/x/sys/windows"

func freeSpace(dir string) (uint64, error) {
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var available uint64
	err = windows.GetDiskFreeSpaceEx(name, &available, nil, nil)
	return available, err
}
