package driver

import (
	"errors"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kubernetes-csi/csi-test/pkg/sanity"
	"github.com/ppc64le-cloud/powervs-csi-driver/pkg/cloud"
	"github.com/ppc64le-cloud/powervs-csi-driver/pkg/util"
	"k8s.io/utils/exec"
	"k8s.io/utils/mount"
)

func TestSanity(t *testing.T) {
	// Setup the full driver and its environment
	dir, err := ioutil.TempDir("", "sanity-ebs-csi")
	if err != nil {
		t.Fatalf("error creating directory %v", err)
	}
	defer os.RemoveAll(dir)

	targetPath := filepath.Join(dir, "mount")
	stagingPath := filepath.Join(dir, "staging")
	endpoint := "unix://" + filepath.Join(dir, "csi.sock")

	config := &sanity.Config{
		TargetPath:       targetPath,
		StagingPath:      stagingPath,
		Address:          endpoint,
		CreateTargetDir:  createDir,
		CreateStagingDir: createDir,
	}

	driverOptions := &Options{
		endpoint: endpoint,
		mode:     AllMode,
	}

	drv := &Driver{
		options: driverOptions,
		controllerService: controllerService{
			cloud:         newFakeCloudProvider(),
			driverOptions: driverOptions,
			volumeLocks:   util.NewVolumeLocks(),
		},
		nodeService: nodeService{
			mounter:       newFakeMounter(),
			cloud:         newFakeCloudProvider(),
			driverOptions: &Options{},
			pvmInstanceId: "test1234",
			volumeLocks:   util.NewVolumeLocks(),
		},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("recover: %v", r)
		}
	}()
	go func() {
		if err := drv.Run(); err != nil {
			panic(fmt.Sprintf("%v", err))
		}
	}()

	// Now call the test suite
	sanity.Test(t, config)
}

func createDir(targetPath string) (string, error) {
	if err := os.MkdirAll(targetPath, 0300); err != nil {
		if os.IsNotExist(err) {
			return "", err
		}
	}
	return targetPath, nil
}

type fakeCloudProvider struct {
	disks  map[string]*fakeDisk
	pub    map[string]string
	tokens map[string]int64
}

type fakeDisk struct {
	*cloud.Disk
}

func newFakeCloudProvider() *fakeCloudProvider {
	return &fakeCloudProvider{
		disks:  make(map[string]*fakeDisk),
		pub:    make(map[string]string),
		tokens: make(map[string]int64),
	}
}

func (p *fakeCloudProvider) GetPVMInstanceByName(name string) (*cloud.PVMInstance, error) {

	return &cloud.PVMInstance{
		ID:      name + "-" + "id",
		ImageID: name + "-" + "image",
		Name:    name,
	}, nil

}

func (p *fakeCloudProvider) GetPVMInstanceByID(instanceID string) (*cloud.PVMInstance, error) {

	return &cloud.PVMInstance{
		ID:      instanceID,
		ImageID: strings.Split(instanceID, "-")[0] + "-" + "image",
		Name:    strings.Split(instanceID, "-")[0],
	}, nil
}

func (p *fakeCloudProvider) GetImageByID(imageID string) (*cloud.PVMImage, error) {

	return &cloud.PVMImage{
		ID:       imageID,
		Name:     strings.Split(imageID, "-")[0] + "-" + "image",
		DiskType: "tier3",
	}, nil
}

func (c *fakeCloudProvider) CreateDisk(volumeName string, diskOptions *cloud.DiskOptions) (*cloud.Disk, error) {
	r1 := rand.New(rand.NewSource(time.Now().UnixNano()))

	if existingDisk, ok := c.disks[volumeName]; ok {
		//Already Created volume
		if existingDisk.Disk.CapacityGiB != util.BytesToGiB(diskOptions.CapacityBytes) {
			return nil, errors.New("disk Already exists")
		} else {
			return existingDisk.Disk, nil
		}
	}
	d := &fakeDisk{
		Disk: &cloud.Disk{
			VolumeID:    fmt.Sprintf("vol-%d", r1.Uint64()),
			CapacityGiB: util.BytesToGiB(diskOptions.CapacityBytes),
			WWN:         "/fake-path",
		},
	}
	c.disks[volumeName] = d
	return d.Disk, nil
}

func (c *fakeCloudProvider) DeleteDisk(volumeID string) (bool, error) {
	for volName, f := range c.disks {
		if f.Disk.VolumeID == volumeID {
			delete(c.disks, volName)
		}
	}
	return true, nil
}

func (c *fakeCloudProvider) AttachDisk(volumeID, nodeID string) error {
	if _, ok := c.pub[volumeID]; ok {
		return cloud.ErrAlreadyExists
	}
	c.pub[volumeID] = nodeID
	return nil
}

func (c *fakeCloudProvider) DetachDisk(volumeID, nodeID string) error {
	return nil
}

func (c *fakeCloudProvider) IsAttached(volumeID string, nodeID string) (attached bool, err error) {
	return true, nil
}

func (c *fakeCloudProvider) WaitForVolumeState(volumeID, expectedState string) error {
	return nil
}

func (c *fakeCloudProvider) GetDiskByName(name string) (*cloud.Disk, error) {
	var disks []*fakeDisk
	for _, d := range c.disks {
		disks = append(disks, d)
	}
	if len(disks) > 1 {
		return nil, cloud.ErrAlreadyExists
	} else if len(disks) == 1 {

		return disks[0].Disk, nil
	}
	return nil, nil
}

func (c *fakeCloudProvider) GetDiskByID(volumeID string) (*cloud.Disk, error) {
	for _, f := range c.disks {
		if f.Disk.VolumeID == volumeID {
			return f.Disk, nil
		}
	}
	return nil, cloud.ErrNotFound
}

func (c *fakeCloudProvider) IsExistInstance(nodeID string) bool {
	return nodeID == "instanceID"
}

func (c *fakeCloudProvider) ResizeDisk(volumeID string, newSize int64) (int64, error) {
	for volName, f := range c.disks {
		if f.Disk.VolumeID == volumeID {
			c.disks[volName].CapacityGiB = newSize
			return newSize, nil
		}
	}
	return 0, cloud.ErrNotFound
}

type fakeMounter struct {
	mount.SafeFormatAndMount
	exec.Interface
}

func newFakeMounter() *fakeMounter {
	return &fakeMounter{
		mount.SafeFormatAndMount{
			Interface: mount.New(""),
			Exec:      exec.New(),
		},
		exec.New(),
	}
}

func (f *fakeMounter) IsCorruptedMnt(err error) bool {
	return false
}

func (f *fakeMounter) Mount(source string, target string, fstype string, options []string) error {
	return nil
}

func (f *fakeMounter) MountSensitive(source string, target string, fstype string, options []string, sensitiveOptions []string) error {
	return nil
}

func (f *fakeMounter) MountSensitiveWithoutSystemd(source string, target string, fstype string, options []string, sensitiveOptions []string) error {
	return nil
}
func (f *fakeMounter) RescanSCSIBus() error {
	return nil
}
func (f *fakeMounter) Unmount(target string) error {
	return nil
}

func (f *fakeMounter) List() ([]mount.MountPoint, error) {
	return []mount.MountPoint{}, nil
}

func (f *fakeMounter) IsLikelyNotMountPoint(file string) (bool, error) {
	return false, nil
}

func (f *fakeMounter) GetMountRefs(pathname string) ([]string, error) {
	return []string{}, nil
}

func (f *fakeMounter) FormatAndMount(source string, target string, fstype string, options []string) error {
	return nil
}

func (f *fakeMounter) GetDeviceNameFromMount(mountPath string) (string, int, error) {
	return "", 0, nil
}

func (f *fakeMounter) MakeFile(pathname string) error {
	file, err := os.OpenFile(pathname, os.O_CREATE, os.FileMode(0644))
	if err != nil {
		if !os.IsExist(err) {
			return err
		}
	}
	if err = file.Close(); err != nil {
		return err
	}
	return nil
}

func (f *fakeMounter) MakeDir(pathname string) error {
	err := os.MkdirAll(pathname, os.FileMode(0755))
	if err != nil {
		if !os.IsExist(err) {
			return err
		}
	}
	return nil
}

func (f *fakeMounter) ExistsPath(filename string) (bool, error) {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func (f *fakeMounter) NeedResize(source string, path string) (bool, error) {
	return false, nil
}

func (f *fakeMounter) GetDeviceName(mountPath string) (string, int, error) {
	return mount.GetDeviceNameFromMount(f, mountPath)
}

func (f *fakeMounter) GetDevicePath(wwn string) (devicePath string, err error) {
	return wwn, nil
}