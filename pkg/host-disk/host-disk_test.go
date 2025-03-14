/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2018 Red Hat, Inc.
 *
 */

package hostdisk

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strings"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	v1 "kubevirt.io/client-go/api/v1"
	"kubevirt.io/client-go/kubecli"
)

type MockNotifier struct {
	Events chan k8sv1.Event
}

func (m MockNotifier) SendK8sEvent(vmi *v1.VirtualMachineInstance, severity string, reason string, message string) error {
	event := k8sv1.Event{
		InvolvedObject: k8sv1.ObjectReference{
			Namespace: vmi.Namespace,
			Name:      vmi.Name,
		},
		Type:    severity,
		Reason:  reason,
		Message: message,
	}
	m.Events <- event
	return nil
}

var _ = Describe("HostDisk", func() {
	var (
		notifier                   MockNotifier
		tempDir                    string
		hostDiskCreator            DiskImgCreator
		hostDiskCreatorWithReserve DiskImgCreator
	)
	addHostDisk := func(vmi *v1.VirtualMachineInstance, volumeName string, hostDiskType v1.HostDiskType, capacity string) {
		var quantity resource.Quantity

		err := os.Mkdir(path.Join(tempDir, volumeName), 0755)
		if !os.IsExist(err) {
			Expect(err).NotTo(HaveOccurred())
		}

		if capacity != "" {
			quantity, err = resource.ParseQuantity(capacity)
			Expect(err).NotTo(HaveOccurred())
		}

		vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
			Name: volumeName,
			VolumeSource: v1.VolumeSource{
				HostDisk: &v1.HostDisk{
					Path:     path.Join(tempDir, volumeName, "disk.img"),
					Type:     hostDiskType,
					Capacity: quantity,
				},
			},
		})
	}

	createTempDiskImg := func(volumeName string) os.FileInfo {
		imgPath := path.Join(tempDir, volumeName, "disk.img")

		err := os.Mkdir(path.Join(tempDir, volumeName), 0755)
		Expect(err).NotTo(HaveOccurred())

		// 67108864 = 64Mi
		err = createSparseRaw(imgPath, 67108864)
		Expect(err).NotTo(HaveOccurred())

		file, err := os.Stat(imgPath)
		Expect(err).NotTo(HaveOccurred())
		return file
	}

	BeforeEach(func() {
		var err error
		tempDir, err = ioutil.TempDir("", "host-disk-images")
		setDiskDirectory(tempDir)
		Expect(err).NotTo(HaveOccurred())
		notifier = MockNotifier{
			Events: make(chan k8sv1.Event, 10),
		}

		hostDiskCreator = NewHostDiskCreator(notifier, 0, 0)
		hostDiskCreatorWithReserve = NewHostDiskCreator(notifier, 10, 1048576)
	})

	AfterEach(func() {
		os.RemoveAll(tempDir)
	})

	Describe("HostDisk with 'Disk' type", func() {
		It("Should not create a disk.img when it exists", func() {
			By("Creating a disk.img before adding a HostDisk volume")
			tmpDiskImg := createTempDiskImg("volume1")

			By("Creating a new minimal vmi")
			vmi := v1.NewMinimalVMI("fake-vmi")

			By("Adding a HostDisk volume for existing disk.img")
			addHostDisk(vmi, "volume1", v1.HostDiskExists, "")

			By("Executing CreateHostDisks which should not create a disk.img")
			err := hostDiskCreator.Create(vmi)
			Expect(err).NotTo(HaveOccurred())

			// check if disk.img has the same modification time
			// which means that CreateHostDisks function did not create a new disk.img
			hostDiskImg, _ := os.Stat(vmi.Spec.Volumes[0].HostDisk.Path)
			Expect(tmpDiskImg.ModTime()).To(Equal(hostDiskImg.ModTime()))
		})
		It("Should not create a disk.img when it does not exist", func() {
			By("Creating a new minimal vmi")
			vmi := v1.NewMinimalVMI("fake-vmi")

			By("Adding a HostDisk volume")
			addHostDisk(vmi, "volume1", v1.HostDiskExists, "")

			By("Executing CreateHostDisks which should not create disk.img")
			err := hostDiskCreator.Create(vmi)
			Expect(err).NotTo(HaveOccurred())

			// disk.img should not exist
			_, err = os.Stat(vmi.Spec.Volumes[0].HostDisk.Path)
			Expect(true).To(Equal(os.IsNotExist(err)))
		})
	})

	Describe("HostDisk with 'DiskOrCreate' type", func() {
		Context("With multiple HostDisk volumes", func() {
			Context("With non existing disk.img", func() {
				It("Should create disk.img if there is enough space", func() {
					By("Creating a new minimal vmi")
					vmi := v1.NewMinimalVMI("fake-vmi")

					By("Adding a HostDisk volumes")
					addHostDisk(vmi, "volume1", v1.HostDiskExistsOrCreate, "64Mi")
					addHostDisk(vmi, "volume2", v1.HostDiskExistsOrCreate, "128Mi")
					addHostDisk(vmi, "volume3", v1.HostDiskExistsOrCreate, "80Mi")

					By("Executing CreateHostDisks which should create disk.img")
					err := hostDiskCreator.Create(vmi)
					Expect(err).NotTo(HaveOccurred())

					// check if images exist and the size is adequate to requirements
					img1, err := os.Stat(vmi.Spec.Volumes[0].HostDisk.Path)
					Expect(err).NotTo(HaveOccurred())
					Expect(img1.Size()).To(Equal(int64(67108864))) // 64Mi

					img2, err := os.Stat(vmi.Spec.Volumes[1].HostDisk.Path)
					Expect(err).NotTo(HaveOccurred())
					Expect(img2.Size()).To(Equal(int64(134217728))) // 128Mi

					img3, err := os.Stat(vmi.Spec.Volumes[2].HostDisk.Path)
					Expect(err).NotTo(HaveOccurred())
					Expect(img3.Size()).To(Equal(int64(83886080))) // 80Mi
				})
				It("Should stop creating disk images if there is not enough space and should return err", func() {
					By("Creating a new minimal vmi")
					vmi := v1.NewMinimalVMI("fake-vmi")

					By("Adding a HostDisk volumes")
					addHostDisk(vmi, "volume1", v1.HostDiskExistsOrCreate, "64Mi")
					addHostDisk(vmi, "volume2", v1.HostDiskExistsOrCreate, "1E")
					addHostDisk(vmi, "volume3", v1.HostDiskExistsOrCreate, "128Mi")

					By("Executing CreateHostDisks func which should not create a disk.img")
					err := hostDiskCreator.Create(vmi)
					Expect(err).To(HaveOccurred())

					// only first disk.img should be created
					// when there is not enough space anymore
					// function should return err and stop creating images
					img1, err := os.Stat(vmi.Spec.Volumes[0].HostDisk.Path)
					Expect(err).NotTo(HaveOccurred())
					Expect(img1.Size()).To(Equal(int64(67108864))) // 64Mi

					_, err = os.Stat(vmi.Spec.Volumes[1].HostDisk.Path)
					Expect(true).To(Equal(os.IsNotExist(err)))

					_, err = os.Stat(vmi.Spec.Volumes[2].HostDisk.Path)
					Expect(true).To(Equal(os.IsNotExist(err)))
				})

				It("Should NOT subtract reserve if there is enough space on storage for requested size", func(done Done) {
					By("Creating a new minimal vmi")
					vmi := v1.NewMinimalVMI("fake-vmi")

					By("Adding HostDisk volumes")
					addHostDisk(vmi, "volume1", v1.HostDiskExistsOrCreate, "64Mi")

					By("Executing CreateHostDisks func which should create a full-size disk.img")
					err := hostDiskCreatorWithReserve.Create(vmi)
					Expect(err).NotTo(HaveOccurred())

					img1, err := os.Stat(vmi.Spec.Volumes[0].HostDisk.Path)
					Expect(err).NotTo(HaveOccurred())
					Expect(img1.Size()).To(Equal(int64(67108864))) // 64Mi
					close(done)
				}, 5)

				It("Should subtract reserve if there is NOT enough space on storage for requested size", func(done Done) {
					By("Creating a new minimal vmi")
					vmi := v1.NewMinimalVMI("fake-vmi")
					dirAvailable := uint64(64 << 20)

					hostDiskCreatorWithReserve.dirBytesAvailableFunc = func(path string, reserve uint64) (uint64, error) {
						return dirAvailable - reserve, nil
					}

					By("Adding HostDisk volume that is slightly too large for available bytes when reserve is accounted for")
					addHostDisk(vmi, "volume1", v1.HostDiskExistsOrCreate, "64Mi")

					By("Executing CreateHostDisks func which should create disk.img minus reserve")
					err := hostDiskCreatorWithReserve.Create(vmi)
					Expect(err).NotTo(HaveOccurred())

					img1, err := os.Stat(vmi.Spec.Volumes[0].HostDisk.Path)
					Expect(err).NotTo(HaveOccurred())
					Expect(img1.Size()).To(BeNumerically("==", dirAvailable-hostDiskCreatorWithReserve.minimumPVCReserveBytes)) // 64Mi minus reserve
					close(done)
				}, 5)

				It("Should refuse to create disk image if reserve causes image to exceed lessPVCSpaceToleration", func(done Done) {
					By("Creating a new minimal vmi")
					vmi := v1.NewMinimalVMI("fake-vmi")
					dirAvailable := uint64(64 << 20)

					hostDiskCreatorWithReserve.dirBytesAvailableFunc = func(path string, reserve uint64) (uint64, error) {
						return dirAvailable - reserve, nil
					}
					hostDiskCreatorWithReserve.setlessPVCSpaceToleration(1) // 1% of 64Mi, tolerate up to 671088 bytes lost

					By("Adding HostDisk volume that is slightly too large for available bytes when reserve is accounted for")
					addHostDisk(vmi, "volume1", v1.HostDiskExistsOrCreate, "64Mi")

					By("Executing CreateHostDisks func which should NOT create disk.img minus reserve")
					err := hostDiskCreatorWithReserve.Create(vmi)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("unable to create"))

					_, err = os.Stat(vmi.Spec.Volumes[0].HostDisk.Path)
					Expect(true).To(Equal(os.IsNotExist(err)))

					close(done)
				}, 5)

				It("Should take lessPVCSpaceToleration into account when creating disk images", func(done Done) {
					By("Creating a new minimal vmi")
					vmi := v1.NewMinimalVMI("fake-vmi")

					toleration := 5
					hostDiskCreator.setlessPVCSpaceToleration(5)
					size64Mi := uint64(67108864) // 64 Mi

					calcToleratedSize := func(origSize uint64, diff int) uint64 {
						return origSize * (100 - uint64(toleration) + uint64(diff)) / 100
					}

					fakeDirBytesAvailable := func(path string, reserve uint64) (uint64, error) {
						if strings.Contains(path, "volume1") {
							// toleration +1
							return calcToleratedSize(size64Mi, 1), nil
						} else if strings.Contains(path, "volume2") {
							// exact toleration
							return calcToleratedSize(size64Mi, 0), nil
						} else if strings.Contains(path, "volume3") {
							// toleration -1
							return calcToleratedSize(size64Mi, -1), nil
						} else {
							return 0, fmt.Errorf("fix your test please")
						}
					}

					By("Adding HostDisk volumes")
					addHostDisk(vmi, "volume1", v1.HostDiskExistsOrCreate, "64Mi")
					addHostDisk(vmi, "volume2", v1.HostDiskExistsOrCreate, "64Mi")
					addHostDisk(vmi, "volume3", v1.HostDiskExistsOrCreate, "64Mi")

					By("Executing CreateHostDisks func which should not create a disk.img")
					hostDiskCreator.dirBytesAvailableFunc = fakeDirBytesAvailable
					err := hostDiskCreator.Create(vmi)
					Expect(err).To(HaveOccurred())

					// only first and second disk.img should be created, with the exact available size
					// third disk is beyond toleration, function should return err and stop creating images
					img1, err := os.Stat(vmi.Spec.Volumes[0].HostDisk.Path)
					Expect(err).NotTo(HaveOccurred())
					Expect(uint64(img1.Size())).To(Equal(calcToleratedSize(size64Mi, 1))) // 64Mi - (toleration + 1%)

					img2, err := os.Stat(vmi.Spec.Volumes[1].HostDisk.Path)
					Expect(err).NotTo(HaveOccurred())
					Expect(uint64(img2.Size())).To(Equal(calcToleratedSize(size64Mi, 0))) // 64Mi

					_, err = os.Stat(vmi.Spec.Volumes[2].HostDisk.Path)
					Expect(true).To(Equal(os.IsNotExist(err)))

					event := <-notifier.Events
					Expect(event.InvolvedObject.Namespace).To(Equal(vmi.Namespace))
					Expect(event.InvolvedObject.Name).To(Equal(vmi.Name))
					Expect(event.Type).To(Equal(EventTypeToleratedSmallPV))
					Expect(event.Reason).To(Equal(EventReasonToleratedSmallPV))
					Expect(event.Message).To(ContainSubstring("PV size too small"))
					close(done)
				}, 5)

			})
		})
		Context("With existing disk.img", func() {
			It("Should not re-create disk.img", func() {
				By("Creating a disk.img before adding a HostDisk volume")
				tmpDiskImg := createTempDiskImg("volume1")

				By("Creating a new minimal vmi")
				vmi := v1.NewMinimalVMI("fake-vmi")

				By("Adding a HostDisk volume")
				addHostDisk(vmi, "volume1", v1.HostDiskExistsOrCreate, "128Mi")

				By("Executing CreateHostDisks which should not create a disk.img")
				err := hostDiskCreator.Create(vmi)
				Expect(err).NotTo(HaveOccurred())

				// check if disk.img has the same modification time
				// which means that CreateHostDisks function did not create a new disk.img
				capacity := vmi.Spec.Volumes[0].HostDisk.Capacity
				specSize, _ := capacity.AsInt64()
				hostDiskImg, _ := os.Stat(vmi.Spec.Volumes[0].HostDisk.Path)
				Expect(tmpDiskImg.ModTime()).To(Equal(hostDiskImg.ModTime()))
				// check if img has the same size as before
				Expect(tmpDiskImg.Size()).NotTo(Equal(specSize))
				Expect(tmpDiskImg.Size()).To(Equal(int64(67108864)))
			})
		})
	})

	Describe("HostDisk with unknown type", func() {
		It("Should not create a disk.img", func() {
			By("Creating a new minimal vmi")
			vmi := v1.NewMinimalVMI("fake-vmi")

			By("Adding a HostDisk volume with unknown type")
			addHostDisk(vmi, "volume1", "UnknownType", "")

			By("Executing CreateHostDisks which should not create a disk.img")
			err := hostDiskCreator.Create(vmi)
			Expect(err).NotTo(HaveOccurred())

			// disk.img should not exist
			_, err = os.Stat(vmi.Spec.Volumes[0].HostDisk.Path)
			Expect(true).To(Equal(os.IsNotExist(err)))
		})
	})

	Describe("VMI with PVC volume", func() {

		var virtClient *kubecli.MockKubevirtClient

		BeforeEach(func() {
			ctrl := gomock.NewController(GinkgoT())
			virtClient = kubecli.NewMockKubevirtClient(ctrl)
			kubeClient := fake.NewSimpleClientset()
			virtClient.EXPECT().CoreV1().Return(kubeClient.CoreV1()).AnyTimes()
		})

		table.DescribeTable("PVC in", func(mode k8sv1.PersistentVolumeMode, pvcReferenceObj string) {

			pvcName := "madeup"

			By("Creating the PVC")
			namespace := "testns"

			By("Creating a VMI with PVC volume")
			volumeName := "pvc-volume"
			volumes := []v1.Volume{
				{
					Name: volumeName,
					VolumeSource: v1.VolumeSource{
						PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName},
					},
				},
			}

			volumeStatus := []v1.VolumeStatus{
				{
					Name: volumeName,
					PersistentVolumeClaimInfo: &v1.PersistentVolumeClaimInfo{
						VolumeMode: &mode,
					},
				},
			}
			vmi := &v1.VirtualMachineInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testvmi", Namespace: namespace, UID: "1234",
				},
				Spec: v1.VirtualMachineInstanceSpec{Volumes: volumes, Domain: v1.DomainSpec{}},
				Status: v1.VirtualMachineInstanceStatus{
					VolumeStatus: volumeStatus,
				},
			}

			// Add a filesystem to vmi spec to test fs passthrough
			if pvcReferenceObj == "filesystem" {
				vmi.Spec.Domain.Devices.Filesystems = append(vmi.Spec.Domain.Devices.Filesystems, v1.Filesystem{
					Name:     volumeName,
					Virtiofs: &v1.FilesystemVirtiofs{},
				})
			}

			By("Replacing PVCs with hostdisks")
			ReplacePVCByHostDisk(vmi)

			Expect(len(vmi.Spec.Volumes)).To(Equal(1), "There should still be 1 volume")

			if mode == k8sv1.PersistentVolumeFilesystem && pvcReferenceObj == "disk" {
				Expect(vmi.Spec.Volumes[0].HostDisk).NotTo(BeNil(), "There should be a hostdisk volume")
				Expect(vmi.Spec.Volumes[0].HostDisk.Type).To(Equal(v1.HostDiskExistsOrCreate), "Correct hostdisk type")
				Expect(vmi.Spec.Volumes[0].HostDisk.Path).NotTo(BeNil(), "Hostdisk path is filled")
				Expect(vmi.Spec.Volumes[0].HostDisk.Capacity).NotTo(BeNil(), "Hostdisk capacity is filled")

				Expect(vmi.Spec.Volumes[0].PersistentVolumeClaim).To(BeNil(), "There shouldn't be a PVC volume anymore")
			} else if mode == k8sv1.PersistentVolumeBlock && pvcReferenceObj == "disk" {
				Expect(vmi.Spec.Volumes[0].HostDisk).To(BeNil(), "There should be no hostdisk volume")
				Expect(vmi.Spec.Volumes[0].PersistentVolumeClaim).ToNot(BeNil(), "There should still be a PVC volume")
				Expect(vmi.Spec.Volumes[0].PersistentVolumeClaim.ClaimName).To(Equal(pvcName), "There should still be the correct PVC volume")
			} else if mode == k8sv1.PersistentVolumeFilesystem && pvcReferenceObj == "filesystem" {
				Expect(vmi.Spec.Volumes[0].HostDisk).To(BeNil(), "There should be no hostdisk volume")
				Expect(vmi.Spec.Volumes[0].PersistentVolumeClaim).ToNot(BeNil(), "There should still be a PVC volume")
				Expect(vmi.Spec.Volumes[0].PersistentVolumeClaim.ClaimName).To(Equal(pvcName), "There should still be the correct PVC volume")
			} else {
				Fail("unknown PVC mode!")
			}

		},

			table.Entry("filemode", k8sv1.PersistentVolumeFilesystem, "disk"),
			table.Entry("blockmode", k8sv1.PersistentVolumeBlock, "disk"),
			table.Entry("filesystem passthrough", k8sv1.PersistentVolumeFilesystem, "filesystem"),
		)
	})

})
