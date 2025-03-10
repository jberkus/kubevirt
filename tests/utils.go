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
 * Copyright 2017 Red Hat, Inc.
 *
 */

package tests

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	goerrors "errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	expect "github.com/google/goexpect"
	. "github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/config"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	k8sv1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1beta1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	extclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
	netutils "k8s.io/utils/net"

	"kubevirt.io/kubevirt/tests/framework/checks"

	util2 "kubevirt.io/kubevirt/tests/util"

	"kubevirt.io/kubevirt/tests/framework/cleanup"

	"kubevirt.io/kubevirt/pkg/certificates/triple/cert"
	"kubevirt.io/kubevirt/pkg/virt-operator/resource/generate/components"

	"kubevirt.io/kubevirt/pkg/certificates/bootstrap"

	v1 "kubevirt.io/client-go/api/v1"
	"kubevirt.io/client-go/kubecli"
	"kubevirt.io/client-go/log"
	cdiv1 "kubevirt.io/containerized-data-importer/pkg/apis/core/v1beta1"
	"kubevirt.io/kubevirt/pkg/controller"
	kutil "kubevirt.io/kubevirt/pkg/util"
	"kubevirt.io/kubevirt/pkg/util/cluster"
	"kubevirt.io/kubevirt/pkg/util/net/ip"
	virtconfig "kubevirt.io/kubevirt/pkg/virt-config"
	"kubevirt.io/kubevirt/pkg/virt-controller/services"
	launcherApi "kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/api"
	"kubevirt.io/kubevirt/pkg/virt-operator/util"
	"kubevirt.io/kubevirt/pkg/virtctl"
	vmsgen "kubevirt.io/kubevirt/tools/vms-generator/utils"

	"kubevirt.io/kubevirt/tests/console"
	cd "kubevirt.io/kubevirt/tests/containerdisk"
	"kubevirt.io/kubevirt/tests/flags"
	"kubevirt.io/kubevirt/tests/libnet"
	"kubevirt.io/kubevirt/tests/libvmi"

	"github.com/Masterminds/semver"
	"github.com/google/go-github/v32/github"
)

var Config *KubeVirtTestsConfiguration
var KubeVirtDefaultConfig v1.KubeVirtConfiguration
var CDIInsecureRegistryConfig *k8sv1.ConfigMap

type EventType string

const TempDirPrefix = "kubevirt-test"

const (
	defaultEventuallyTimeout         = 5 * time.Second
	defaultEventuallyPollingInterval = 1 * time.Second
)

const (
	AlpineHttpUrl = iota
	DummyFileHttpUrl
	CirrosHttpUrl
	VirtWhatCpuidHelperHttpUrl
)

const (
	NormalEvent  EventType = "Normal"
	WarningEvent EventType = "Warning"
)

const defaultTestGracePeriod int64 = 0

const (
	SubresourceServiceAccountName = "kubevirt-subresource-test-sa"
	AdminServiceAccountName       = "kubevirt-admin-test-sa"
	EditServiceAccountName        = "kubevirt-edit-test-sa"
	ViewServiceAccountName        = "kubevirt-view-test-sa"
)

const SubresourceTestLabel = "subresource-access-test-pod"
const insecureRegistryConfigName = "cdi-insecure-registries"

// NamespaceTestAlternative is used to test controller-namespace independency.
var NamespaceTestAlternative = "kubevirt-test-alternative"

// NamespaceTestOperator is used to test if namespaces can still be deleted when kubevirt is uninstalled
var NamespaceTestOperator = "kubevirt-test-operator"

const (
	ISCSITargetName = "test-isci-target"
)

var TestNamespaces = []string{util2.NamespaceTestDefault, NamespaceTestAlternative, NamespaceTestOperator}
var schedulableNode = ""

type startType string

const (
	invalidWatch startType = "invalidWatch"
	// Watch since the moment a long poll connection is established
	watchSinceNow startType = "watchSinceNow"
	// Watch since the resourceVersion of the passed in runtime object
	watchSinceObjectUpdate startType = "watchSinceObjectUpdate"
	// Watch since the resourceVersion of the watched object
	watchSinceWatchedObjectUpdate startType = "watchSinceWatchedObjectUpdate"
	// Watch since the resourceVersion passed in to the builder
	watchSinceResourceVersion startType = "watchSinceResourceVersion"
)

const (
	osAlpineHostPath = "alpine-host-path"
	OSWindows        = "windows"
	OSWindowsSysprep = "windows-sysprep" // This is for sysprep tests, they run on a syspreped image of windows of a different version.
	OSRhel           = "rhel"
	CustomHostPath   = "custom-host-path"
	HostPathBase     = "/tmp/hostImages"
)

var (
	HostPathAlpine string
	HostPathCustom string
)

const (
	DiskAlpineHostPath = "disk-alpine-host-path"
	DiskWindows        = "disk-windows"
	DiskWindowsSysprep = "disk-windows-sysprep"
	DiskRhel           = "disk-rhel"
	DiskCustomHostPath = "disk-custom-host-path"
)

const (
	defaultDiskSize = "1Gi"
)

const VMIResource = "virtualmachineinstances"

const (
	SecretLabel = "kubevirt.io/secret"
)

const (
	IstioInjectNamespaceLabel = "istio-injection"
)

var (
	// BlockDiskForTest contains name of the block PV and PVC
	BlockDiskForTest string
)

const (
	tmpPath = "/var/provision/kubevirt.io/tests"
)

const (
	capNetRaw         k8sv1.Capability = "NET_RAW"
	capSysNice        k8sv1.Capability = "SYS_NICE"
	capNetBindService k8sv1.Capability = "NET_BIND_SERVICE"
)

const MigrationWaitTime = 240
const ContainerCompletionWaitTime = 60

type ProcessFunc func(event *k8sv1.Event) (done bool)

type ObjectEventWatcher struct {
	object                 runtime.Object
	timeout                *time.Duration
	resourceVersion        string
	startType              startType
	warningPolicy          WarningsPolicy
	dontFailOnMissingEvent bool
}

type WarningsPolicy struct {
	FailOnWarnings     bool
	WarningsIgnoreList []string
}

func (wp *WarningsPolicy) shouldIgnoreWarning(event *k8sv1.Event) bool {
	if event.Type == string(WarningEvent) {
		for _, message := range wp.WarningsIgnoreList {
			if message == event.Message {
				return true
			}
		}
	}

	return false
}

func NewObjectEventWatcher(object runtime.Object) *ObjectEventWatcher {
	return &ObjectEventWatcher{object: object, startType: invalidWatch}
}

func (w *ObjectEventWatcher) Timeout(duration time.Duration) *ObjectEventWatcher {
	w.timeout = &duration
	return w
}

func (w *ObjectEventWatcher) SetWarningsPolicy(wp WarningsPolicy) *ObjectEventWatcher {
	w.warningPolicy = wp
	return w
}

/*
SinceNow sets a watch starting point for events, from the moment on the connection to the apiserver
was established.
*/
func (w *ObjectEventWatcher) SinceNow() *ObjectEventWatcher {
	w.startType = watchSinceNow
	return w
}

/*
SinceWatchedObjectResourceVersion takes the resource version of the runtime object which is watched,
and takes it as the starting point for all events to watch for.
*/
func (w *ObjectEventWatcher) SinceWatchedObjectResourceVersion() *ObjectEventWatcher {
	w.startType = watchSinceWatchedObjectUpdate
	return w
}

/*
SinceObjectResourceVersion takes the resource version of the passed in runtime object and takes it
as the starting point for all events to watch for.
*/
func (w *ObjectEventWatcher) SinceObjectResourceVersion(object runtime.Object) *ObjectEventWatcher {
	var err error
	w.startType = watchSinceObjectUpdate
	w.resourceVersion, err = meta.NewAccessor().ResourceVersion(object)
	Expect(err).ToNot(HaveOccurred())
	return w
}

/*
SinceResourceVersion sets the passed in resourceVersion as the starting point for all events to watch for.
*/
func (w *ObjectEventWatcher) SinceResourceVersion(rv string) *ObjectEventWatcher {
	w.resourceVersion = rv
	w.startType = watchSinceResourceVersion
	return w
}

func (w *ObjectEventWatcher) Watch(ctx context.Context, processFunc ProcessFunc, watchedDescription string) {
	Expect(w.startType).ToNot(Equal(invalidWatch))
	resourceVersion := ""

	switch w.startType {
	case watchSinceNow:
		resourceVersion = ""
	case watchSinceObjectUpdate, watchSinceResourceVersion:
		resourceVersion = w.resourceVersion
	case watchSinceWatchedObjectUpdate:
		var err error
		resourceVersion, err = meta.NewAccessor().ResourceVersion(w.object)
		Expect(err).ToNot(HaveOccurred())
	}

	cli, err := kubecli.GetKubevirtClient()
	if err != nil {
		panic(err)
	}

	f := processFunc

	if w.warningPolicy.FailOnWarnings {
		f = func(event *k8sv1.Event) bool {
			msg := fmt.Sprintf("Event(%#v): type: '%v' reason: '%v' %v", event.InvolvedObject, event.Type, event.Reason, event.Message)
			if w.warningPolicy.shouldIgnoreWarning(event) == false {
				ExpectWithOffset(1, event.Type).NotTo(Equal(string(WarningEvent)), "Unexpected Warning event received: %s,%s: %s", event.InvolvedObject.Name, event.InvolvedObject.UID, event.Message)
			}
			log.Log.ObjectRef(&event.InvolvedObject).Info(msg)

			return processFunc(event)
		}
	} else {
		f = func(event *k8sv1.Event) bool {
			if event.Type == string(WarningEvent) {
				log.Log.ObjectRef(&event.InvolvedObject).Reason(fmt.Errorf("Warning event received")).Error(event.Message)
			} else {
				log.Log.ObjectRef(&event.InvolvedObject).Infof(event.Message)
			}
			return processFunc(event)
		}
	}

	var selector []string
	objectMeta := w.object.(metav1.ObjectMetaAccessor)
	name := objectMeta.GetObjectMeta().GetName()
	namespace := objectMeta.GetObjectMeta().GetNamespace()
	uid := objectMeta.GetObjectMeta().GetUID()

	selector = append(selector, fmt.Sprintf("involvedObject.name=%v", name))
	if namespace != "" {
		selector = append(selector, fmt.Sprintf("involvedObject.namespace=%v", namespace))
	}
	if uid != "" {
		selector = append(selector, fmt.Sprintf("involvedObject.uid=%v", uid))
	}

	eventWatcher, err := cli.CoreV1().Events(k8sv1.NamespaceAll).
		Watch(context.Background(), metav1.ListOptions{
			FieldSelector:   fields.ParseSelectorOrDie(strings.Join(selector, ",")).String(),
			ResourceVersion: resourceVersion,
		})
	if err != nil {
		panic(err)
	}
	defer eventWatcher.Stop()
	done := make(chan struct{})

	go func() {
		defer GinkgoRecover()
		for watchEvent := range eventWatcher.ResultChan() {
			if watchEvent.Type != watch.Error {
				event := watchEvent.Object.(*k8sv1.Event)
				if f(event) {
					close(done)
					break
				}
			} else {
				Fail(fmt.Sprintf("unexpected error event: %v", apierrors.FromObject(watchEvent.Object)))
			}
		}
	}()

	if w.timeout != nil {
		select {
		case <-done:
		case <-ctx.Done():
		case <-time.After(*w.timeout):
			if !w.dontFailOnMissingEvent {
				Fail(fmt.Sprintf("Waited for %v seconds on the event stream to match a specific event: %s", w.timeout.Seconds(), watchedDescription), 1)
			}
		}
	} else {
		select {
		case <-ctx.Done():
		case <-done:
		}
	}
}

func (w *ObjectEventWatcher) WaitFor(ctx context.Context, eventType EventType, reason interface{}) (e *k8sv1.Event) {
	w.Watch(ctx, func(event *k8sv1.Event) bool {
		if event.Type == string(eventType) && event.Reason == reflect.ValueOf(reason).String() {
			e = event
			return true
		}
		return false
	}, fmt.Sprintf("event type %s, reason = %s", string(eventType), reflect.ValueOf(reason).String()))
	return
}

func (w *ObjectEventWatcher) WaitNotFor(ctx context.Context, eventType EventType, reason interface{}) (e *k8sv1.Event) {
	w.dontFailOnMissingEvent = true
	w.Watch(ctx, func(event *k8sv1.Event) bool {
		if event.Type == string(eventType) && event.Reason == reflect.ValueOf(reason).String() {
			e = event
			Fail(fmt.Sprintf("Did not expect %s with reason %s", string(eventType), reflect.ValueOf(reason).String()), 1)
			return true
		}
		return false
	}, fmt.Sprintf("not happen event type %s, reason = %s", string(eventType), reflect.ValueOf(reason).String()))
	return
}

func WaitForAllPodsReady(timeout time.Duration, listOptions metav1.ListOptions) {
	checkForPodsToBeReady := func() []string {
		podsNotReady := make([]string, 0)
		virtClient, err := kubecli.GetKubevirtClient()
		util2.PanicOnError(err)

		podsList, err := virtClient.CoreV1().Pods(k8sv1.NamespaceAll).List(context.Background(), listOptions)
		util2.PanicOnError(err)
		for _, pod := range podsList.Items {
			for _, status := range pod.Status.ContainerStatuses {
				if status.State.Terminated != nil {
					break // We don't care about terminated pods
				} else if status.State.Running != nil {
					if !status.Ready { // We need to wait for this one
						podsNotReady = append(podsNotReady, pod.Name)
						break
					}
				} else {
					// It is in Waiting state, We need to wait for this one
					podsNotReady = append(podsNotReady, pod.Name)
					break
				}
			}
		}
		return podsNotReady
	}
	Eventually(checkForPodsToBeReady, timeout, 2*time.Second).Should(BeEmpty(), "There are pods in system which are not ready.")
}

func SynchronizedAfterTestSuiteCleanup() {
	RestoreKubeVirtResource()

	if Config.ManageStorageClasses {
		deleteStorageClass(Config.StorageClassHostPath)
		deleteStorageClass(Config.StorageClassBlockVolume)
	}
	CleanNodes()
}

func AfterTestSuitCleanup() {

	cleanupServiceAccounts()
	cleanNamespaces()

	if flags.DeployTestingInfrastructureFlag {
		WipeTestingInfrastructure()
	}
	removeNamespaces()
}

func BeforeTestCleanup() {
	deleteBlockPVAndPVC()
	cleanNamespaces()
	CleanNodes()
	resetToDefaultConfig()
	CreateHostPathPv(osAlpineHostPath, HostPathAlpine)
	CreateHostPathPVC(osAlpineHostPath, defaultDiskSize)
}

func CleanNodes() {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)
	nodes := util2.GetAllSchedulableNodes(virtCli).Items

	clusterDrainKey := GetNodeDrainKey()

	for _, node := range nodes {

		old, err := json.Marshal(node)
		Expect(err).ToNot(HaveOccurred())
		new := node.DeepCopy()

		k8sClient := GetK8sCmdClient()
		if k8sClient == "oc" {
			RunCommandWithNS("", k8sClient, "adm", "uncordon", node.Name)
		} else {
			RunCommandWithNS("", k8sClient, "uncordon", node.Name)
		}

		found := false
		taints := []k8sv1.Taint{}
		for _, taint := range node.Spec.Taints {

			if taint.Key == clusterDrainKey && taint.Effect == k8sv1.TaintEffectNoSchedule {
				found = true
			} else if taint.Key == "kubevirt.io/drain" && taint.Effect == k8sv1.TaintEffectNoSchedule {
				// this key is used as a fallback if the original drain key is built-in
				found = true
			} else if taint.Key == "kubevirt.io/alt-drain" && taint.Effect == k8sv1.TaintEffectNoSchedule {
				// this key is used in testing as a custom alternate drain key
				found = true
			} else {
				taints = append(taints, taint)
			}

		}
		new.Spec.Taints = taints

		for k := range node.Labels {
			if strings.HasPrefix(k, "tests.kubevirt.io") {
				found = true
				delete(new.Labels, k)
			}
		}

		if node.Spec.Unschedulable {
			new.Spec.Unschedulable = false
		}

		if !found {
			continue
		}
		newJson, err := json.Marshal(new)
		Expect(err).ToNot(HaveOccurred())

		patch, err := strategicpatch.CreateTwoWayMergePatch(old, newJson, node)
		Expect(err).ToNot(HaveOccurred())

		_, err = virtCli.CoreV1().Nodes().Patch(context.Background(), node.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
		Expect(err).ToNot(HaveOccurred())
	}
}

func AddLabelToNode(nodeName string, key string, value string) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)
	node, err := virtCli.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred())

	old, err := json.Marshal(node)
	Expect(err).ToNot(HaveOccurred())
	new := node.DeepCopy()
	new.Labels[key] = value

	newJson, err := json.Marshal(new)
	Expect(err).ToNot(HaveOccurred())

	patch, err := strategicpatch.CreateTwoWayMergePatch(old, newJson, node)
	Expect(err).ToNot(HaveOccurred())

	_, err = virtCli.CoreV1().Nodes().Patch(context.Background(), node.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	Expect(err).ToNot(HaveOccurred())
}

func RemoveLabelFromNode(nodeName string, key string) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)
	node, err := virtCli.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred())

	if _, exists := node.Labels[key]; !exists {
		return
	}

	old, err := json.Marshal(node)
	Expect(err).ToNot(HaveOccurred())
	new := node.DeepCopy()
	delete(new.Labels, key)

	newJson, err := json.Marshal(new)
	Expect(err).ToNot(HaveOccurred())

	patch, err := strategicpatch.CreateTwoWayMergePatch(old, newJson, node)
	Expect(err).ToNot(HaveOccurred())

	_, err = virtCli.CoreV1().Nodes().Patch(context.Background(), node.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	Expect(err).ToNot(HaveOccurred())
}

func Taint(nodeName string, key string, effect k8sv1.TaintEffect) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)
	node, err := virtCli.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred())

	old, err := json.Marshal(node)
	Expect(err).ToNot(HaveOccurred())
	new := node.DeepCopy()
	new.Spec.Taints = append(new.Spec.Taints, k8sv1.Taint{
		Key:    key,
		Effect: effect,
	})

	newJson, err := json.Marshal(new)
	Expect(err).ToNot(HaveOccurred())

	patch, err := strategicpatch.CreateTwoWayMergePatch(old, newJson, node)
	Expect(err).ToNot(HaveOccurred())

	_, err = virtCli.CoreV1().Nodes().Patch(context.Background(), node.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	Expect(err).ToNot(HaveOccurred())
}

// CalculateNamespaces checks on which ginkgo gest node the tests are run and sets the namespaces accordingly
func CalculateNamespaces() {
	worker := config.GinkgoConfig.ParallelNode
	util2.NamespaceTestDefault = fmt.Sprintf("%s%d", util2.NamespaceTestDefault, worker)
	NamespaceTestAlternative = fmt.Sprintf("%s%d", NamespaceTestAlternative, worker)
	// TODO, that is not needed, just a shortcut to not have to treat this namespace
	// differently when running in parallel
	NamespaceTestOperator = fmt.Sprintf("%s%d", NamespaceTestOperator, worker)
	TestNamespaces = []string{util2.NamespaceTestDefault, NamespaceTestAlternative, NamespaceTestOperator}
}

func SynchronizedBeforeTestSetup() []byte {
	var err error
	Config, err = loadConfig()
	Expect(err).ToNot(HaveOccurred())

	if flags.KubeVirtInstallNamespace == "" {
		detectInstallNamespace()
	}

	if flags.DeployTestingInfrastructureFlag {
		WipeTestingInfrastructure()
		DeployTestingInfrastructure()
	}

	if Config.ManageStorageClasses {
		createStorageClass(Config.StorageClassHostPath)
		createStorageClass(Config.StorageClassBlockVolume)
	}

	EnsureKVMPresent()
	AdjustKubeVirtResource()

	return nil
}

func BeforeTestSuitSetup(_ []byte) {
	rand.Seed(int64(config.GinkgoConfig.ParallelNode))
	log.InitializeLogging("tests")
	log.Log.SetIOWriter(GinkgoWriter)
	var err error
	Config, err = loadConfig()
	Expect(err).ToNot(HaveOccurred())

	// Customize host disk paths
	// Right now we support three nodes. More image copying needs to happen
	// TODO link this somehow with the image provider which we run upfront
	worker := config.GinkgoConfig.ParallelNode
	HostPathAlpine = filepath.Join(HostPathBase, fmt.Sprintf("%s%v", "alpine", worker))
	HostPathCustom = filepath.Join(HostPathBase, fmt.Sprintf("%s%v", "custom", worker))

	BlockDiskForTest = fmt.Sprintf("block-disk-for-tests%v", worker)

	// Wait for schedulable nodes
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)
	Eventually(func() int {
		nodes := util2.GetAllSchedulableNodes(virtClient)
		if len(nodes.Items) > 0 {
			idx := rand.Intn(len(nodes.Items))
			schedulableNode = nodes.Items[idx].Name
		}
		return len(nodes.Items)
	}, 5*time.Minute, 10*time.Second).ShouldNot(BeZero(), "no schedulable nodes found")

	createNamespaces()
	createServiceAccounts()

	SetDefaultEventuallyTimeout(defaultEventuallyTimeout)
	SetDefaultEventuallyPollingInterval(defaultEventuallyPollingInterval)
}

var originalKV *v1.KubeVirt

func AdjustKubeVirtResource() {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	kv := util2.GetCurrentKv(virtClient)
	originalKV = kv.DeepCopy()

	KubeVirtDefaultConfig = originalKV.Spec.Configuration

	if !flags.ApplyDefaulte2eConfiguration {
		return
	}

	// Rotate very often during the tests to ensure that things are working
	kv.Spec.CertificateRotationStrategy = v1.KubeVirtCertificateRotateStrategy{SelfSigned: &v1.KubeVirtSelfSignConfiguration{
		CA: &v1.CertConfig{
			Duration:    &metav1.Duration{Duration: 20 * time.Minute},
			RenewBefore: &metav1.Duration{Duration: 12 * time.Minute},
		},
		Server: &v1.CertConfig{
			Duration:    &metav1.Duration{Duration: 14 * time.Minute},
			RenewBefore: &metav1.Duration{Duration: 10 * time.Minute},
		},
	}}

	// match default kubevirt-config testing resource
	if kv.Spec.Configuration.DeveloperConfiguration == nil {
		kv.Spec.Configuration.DeveloperConfiguration = &v1.DeveloperConfiguration{}
	}

	if kv.Spec.Configuration.DeveloperConfiguration.FeatureGates == nil {
		kv.Spec.Configuration.DeveloperConfiguration.FeatureGates = []string{}
	}
	kv.Spec.Configuration.DeveloperConfiguration.FeatureGates = append(kv.Spec.Configuration.DeveloperConfiguration.FeatureGates,
		virtconfig.CPUManager,
		virtconfig.LiveMigrationGate,
		virtconfig.IgnitionGate,
		virtconfig.SidecarGate,
		virtconfig.SnapshotGate,
		virtconfig.HostDiskGate,
		virtconfig.VirtIOFSGate,
		virtconfig.HotplugVolumesGate,
		virtconfig.DownwardMetricsFeatureGate,
		virtconfig.NUMAFeatureGate,
	)
	kv.Spec.Configuration.SELinuxLauncherType = "virt_launcher.process"

	data, err := json.Marshal(kv.Spec)
	Expect(err).ToNot(HaveOccurred())
	patchData := fmt.Sprintf(`[{ "op": "replace", "path": "/spec", "value": %s }]`, string(data))
	adjustedKV, err := virtClient.KubeVirt(kv.Namespace).Patch(kv.Name, types.JSONPatchType, []byte(patchData))
	util2.PanicOnError(err)
	KubeVirtDefaultConfig = adjustedKV.Spec.Configuration
	CDIInsecureRegistryConfig, err = virtClient.CoreV1().ConfigMaps(flags.ContainerizedDataImporterNamespace).Get(context.Background(), insecureRegistryConfigName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			// force it to nil, independent of what the client returned
			CDIInsecureRegistryConfig = nil
		} else {
			util2.PanicOnError(err)
		}
	}
}

func RestoreKubeVirtResource() {
	if originalKV != nil {
		virtClient, err := kubecli.GetKubevirtClient()
		util2.PanicOnError(err)
		data, err := json.Marshal(originalKV.Spec)
		Expect(err).ToNot(HaveOccurred())
		patchData := fmt.Sprintf(`[{ "op": "replace", "path": "/spec", "value": %s }]`, string(data))
		_, err = virtClient.KubeVirt(originalKV.Namespace).Patch(originalKV.Name, types.JSONPatchType, []byte(patchData))
		util2.PanicOnError(err)
	}
}

func createStorageClass(name string) {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	sc := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"kubevirt.io/test": name,
			},
		},
		Provisioner: name,
	}
	_, err = virtClient.StorageV1().StorageClasses().Create(context.Background(), sc, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}
}

func deleteStorageClass(name string) {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	_, err = virtClient.StorageV1().StorageClasses().Get(context.Background(), name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return
	}
	util2.PanicOnError(err)

	err = virtClient.StorageV1().StorageClasses().Delete(context.Background(), name, metav1.DeleteOptions{})
	util2.PanicOnError(err)
}

func ShouldUseEmulation(virtClient kubecli.KubevirtClient) bool {
	useEmulation := false
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	kv := util2.GetCurrentKv(virtClient)
	if kv.Spec.Configuration.DeveloperConfiguration != nil {
		useEmulation = kv.Spec.Configuration.DeveloperConfiguration.UseEmulation
	}

	return useEmulation
}

func EnsureKVMPresent() {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	if !ShouldUseEmulation(virtClient) {
		listOptions := metav1.ListOptions{LabelSelector: v1.AppLabel + "=virt-handler"}
		virtHandlerPods, err := virtClient.CoreV1().Pods(flags.KubeVirtInstallNamespace).List(context.Background(), listOptions)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		EventuallyWithOffset(1, func() bool {
			ready := true
			// cluster is not ready until all nodes are ready.
			for _, pod := range virtHandlerPods.Items {
				virtHandlerNode, err := virtClient.CoreV1().Nodes().Get(context.Background(), pod.Spec.NodeName, metav1.GetOptions{})
				ExpectWithOffset(1, err).ToNot(HaveOccurred())

				kvmAllocatable, ok1 := virtHandlerNode.Status.Allocatable[services.KvmDevice]
				vhostNetAllocatable, ok2 := virtHandlerNode.Status.Allocatable[services.VhostNetDevice]
				ready = ready && ok1 && ok2
				ready = ready && (kvmAllocatable.Value() > 0) && (vhostNetAllocatable.Value() > 0)
			}
			return ready
		}, 120*time.Second, 1*time.Second).Should(BeTrue(),
			"Both KVM devices and vhost-net devices are required for testing, but are not present on cluster nodes")
	}
}

func GetNodesWithKVM() []*k8sv1.Node {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)
	listOptions := metav1.ListOptions{LabelSelector: v1.AppLabel + "=virt-handler"}
	virtHandlerPods, err := virtClient.CoreV1().Pods(flags.KubeVirtInstallNamespace).List(context.Background(), listOptions)
	Expect(err).ToNot(HaveOccurred())

	nodes := make([]*k8sv1.Node, 0)
	// cluster is not ready until all nodes are ready.
	for _, pod := range virtHandlerPods.Items {
		virtHandlerNode, err := virtClient.CoreV1().Nodes().Get(context.Background(), pod.Spec.NodeName, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())

		_, ok := virtHandlerNode.Status.Allocatable[services.KvmDevice]
		if ok {
			nodes = append(nodes, virtHandlerNode)
		}
	}
	return nodes
}

func GetSupportedCPUFeatures(nodes k8sv1.NodeList) []string {
	var featureDenyList = map[string]bool{
		"svm": true,
	}
	featuresMap := make(map[string]bool)
	for _, node := range nodes.Items {
		for key := range node.Labels {
			if strings.Contains(key, services.NFD_CPU_FEATURE_PREFIX) {
				feature := strings.TrimPrefix(key, services.NFD_CPU_FEATURE_PREFIX)
				if _, ok := featureDenyList[feature]; !ok {
					featuresMap[feature] = true
				}
			}
		}
	}

	features := make([]string, 0)
	for feature := range featuresMap {
		features = append(features, feature)
	}
	return features
}

func GetSupportedCPUModels(nodes k8sv1.NodeList) []string {
	var cpuDenyList = map[string]bool{
		"qemu64":     true,
		"Opteron_G2": true,
	}
	cpuMap := make(map[string]bool)
	for _, node := range nodes.Items {
		for key := range node.Labels {
			if strings.Contains(key, services.NFD_CPU_MODEL_PREFIX) {
				cpu := strings.TrimPrefix(key, services.NFD_CPU_MODEL_PREFIX)
				if _, ok := cpuDenyList[cpu]; !ok {
					cpuMap[cpu] = true
				}
			}
		}
	}

	cpus := make([]string, 0)
	for model := range cpuMap {
		cpus = append(cpus, model)
	}
	return cpus
}

func CreateConfigMap(name string, data map[string]string) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)
	_, err = virtCli.CoreV1().ConfigMaps(util2.NamespaceTestDefault).Create(context.Background(), &k8sv1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Data:       data,
	}, metav1.CreateOptions{})

	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}
}

func CreateSecret(name string, data map[string]string) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	_, err = virtCli.CoreV1().Secrets(util2.NamespaceTestDefault).Create(context.Background(), &k8sv1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		StringData: data,
	}, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}
}

func CreateHostPathPVC(os, size string) {
	CreatePVC(os, size, Config.StorageClassHostPath, false)
}

func CreatePVC(os, size, storageClass string, recycledPV bool) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	_, err = virtCli.CoreV1().PersistentVolumeClaims((util2.NamespaceTestDefault)).Create(context.Background(), newPVC(os, size, storageClass, recycledPV), metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}
}

func CreateRuntimeClass(name, handler string) (*nodev1.RuntimeClass, error) {
	virtCli, err := kubecli.GetKubevirtClient()
	if err != nil {
		return nil, err
	}

	return virtCli.NodeV1beta1().RuntimeClasses().Create(
		context.Background(),
		&nodev1.RuntimeClass{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Handler:    handler,
		},
		metav1.CreateOptions{},
	)
}

func DeleteRuntimeClass(name string) error {
	virtCli, err := kubecli.GetKubevirtClient()
	if err != nil {
		return err
	}

	return virtCli.NodeV1beta1().RuntimeClasses().Delete(context.Background(), name, metav1.DeleteOptions{})
}

func newPVC(os, size, storageClass string, recycledPV bool) *k8sv1.PersistentVolumeClaim {
	quantity, err := resource.ParseQuantity(size)
	util2.PanicOnError(err)

	name := fmt.Sprintf("disk-%s", os)

	selector := map[string]string{
		"kubevirt.io/test": os,
	}

	// If the PV is not recycled, it will have a namespace related test label which  we should match
	if !recycledPV {
		selector[cleanup.TestLabelForNamespace(util2.NamespaceTestDefault)] = ""
	}

	return &k8sv1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: k8sv1.PersistentVolumeClaimSpec{
			AccessModes: []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce},
			Resources: k8sv1.ResourceRequirements{
				Requests: k8sv1.ResourceList{
					"storage": quantity,
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: selector,
			},
			StorageClassName: &storageClass,
		},
	}
}

func CreateHostPathPv(osName string, hostPath string) string {
	return CreateHostPathPvWithSize(osName, hostPath, "1Gi")
}

func CreateHostPathPvWithSize(osName string, hostPath string, size string) string {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	quantity, err := resource.ParseQuantity(size)
	util2.PanicOnError(err)

	hostPathType := k8sv1.HostPathDirectoryOrCreate

	name := fmt.Sprintf("%s-disk-for-tests", osName)
	pv := &k8sv1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-%s", name, util2.NamespaceTestDefault),
			Labels: map[string]string{
				"kubevirt.io/test": osName,
				cleanup.TestLabelForNamespace(util2.NamespaceTestDefault): "",
			},
		},
		Spec: k8sv1.PersistentVolumeSpec{
			AccessModes: []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce},
			Capacity: k8sv1.ResourceList{
				"storage": quantity,
			},
			PersistentVolumeReclaimPolicy: k8sv1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource: k8sv1.PersistentVolumeSource{
				HostPath: &k8sv1.HostPathVolumeSource{
					Path: hostPath,
					Type: &hostPathType,
				},
			},
			StorageClassName: Config.StorageClassHostPath,
			NodeAffinity: &k8sv1.VolumeNodeAffinity{
				Required: &k8sv1.NodeSelector{
					NodeSelectorTerms: []k8sv1.NodeSelectorTerm{
						{
							MatchExpressions: []k8sv1.NodeSelectorRequirement{
								{
									Key:      "kubernetes.io/hostname",
									Operator: k8sv1.NodeSelectorOpIn,
									Values:   []string{schedulableNode},
								},
							},
						},
					},
				},
			},
		},
	}

	_, err = virtCli.CoreV1().PersistentVolumes().Create(context.Background(), pv, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}
	return schedulableNode
}

func GetListOfManifests(pathToManifestsDir string) []string {
	var manifests []string
	matchFileName := func(pattern, filename string) bool {
		match, err := filepath.Match(pattern, filename)
		if err != nil {
			panic(err)
		}
		return match
	}
	err := filepath.Walk(pathToManifestsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			fmt.Printf("ERROR: Can not access a path %q: %v\n", path, err)
			return err
		}
		if !info.IsDir() && matchFileName("*.yaml", info.Name()) {
			manifests = append(manifests, path)
		}
		return nil
	})
	if err != nil {
		fmt.Printf("ERROR: Walking the path %q: %v\n", pathToManifestsDir, err)
		panic(err)
	}
	return manifests
}

func ReadManifestYamlFile(pathToManifest string) []unstructured.Unstructured {
	var objects []unstructured.Unstructured
	stream, err := os.Open(pathToManifest)
	util2.PanicOnError(err)

	decoder := yaml.NewYAMLOrJSONDecoder(stream, 1024)
	for {
		obj := map[string]interface{}{}
		err := decoder.Decode(&obj)
		if err == io.EOF {
			break
		} else if err != nil {
			panic(err)
		}
		if len(obj) == 0 {
			continue
		}
		objects = append(objects, unstructured.Unstructured{Object: obj})
	}
	return objects
}

func isNamespaceScoped(kind schema.GroupVersionKind) bool {
	switch kind.Kind {
	case "ClusterRole", "ClusterRoleBinding":
		return false
	}
	return true
}

func IsOpenShift() bool {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	isOpenShift, err := cluster.IsOnOpenShift(virtClient)
	if err != nil {
		fmt.Printf("ERROR: Can not determine cluster type %v\n", err)
		panic(err)
	}

	return isOpenShift
}

func ServiceMonitorEnabled() bool {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	serviceMonitorEnabled, err := util.IsServiceMonitorEnabled(virtClient)
	if err != nil {
		fmt.Printf("ERROR: Can't verify ServiceMonitor CRD %v\n", err)
		panic(err)
	}

	return serviceMonitorEnabled
}

// PrometheusRuleEnabled returns true if the PrometheusRule CRD is enabled
// and false otherwise.
func PrometheusRuleEnabled() bool {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	prometheusRuleEnabled, err := util.IsPrometheusRuleEnabled(virtClient)
	if err != nil {
		fmt.Printf("ERROR: Can't verify PrometheusRule CRD %v\n", err)
		panic(err)
	}

	return prometheusRuleEnabled
}

func composeResourceURI(object unstructured.Unstructured) string {
	uri := "/api"
	if object.GetAPIVersion() != "v1" {
		uri += "s"
	}
	uri += "/" + object.GetAPIVersion()
	if object.GetNamespace() != "" && isNamespaceScoped(object.GroupVersionKind()) {
		uri += "/namespaces/" + object.GetNamespace()
	}
	uri += "/" + strings.ToLower(object.GetKind())
	if !strings.HasSuffix(object.GetKind(), "s") {
		uri += "s"
	}
	return uri
}

func ApplyRawManifest(object unstructured.Unstructured) error {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	uri := composeResourceURI(object)
	jsonbody, err := object.MarshalJSON()
	util2.PanicOnError(err)
	b, err := virtCli.CoreV1().RESTClient().Post().RequestURI(uri).Body(jsonbody).DoRaw(context.Background())
	if err != nil {
		fmt.Printf(fmt.Sprintf("ERROR: Can not apply %s\n", object))
		panic(err)
	}
	status := unstructured.Unstructured{}
	return json.Unmarshal(b, &status)
}

func DeleteRawManifest(object unstructured.Unstructured) error {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	uri := composeResourceURI(object)
	uri = uri + "/" + object.GetName()

	policy := metav1.DeletePropagationBackground
	options := &metav1.DeleteOptions{PropagationPolicy: &policy}

	result := virtCli.CoreV1().RESTClient().Delete().RequestURI(uri).Body(options).Do(context.Background())
	if result.Error() != nil && !errors.IsNotFound(result.Error()) {
		fmt.Printf(fmt.Sprintf("ERROR: Can not delete %s err: %#v %s\n", object.GetName(), result.Error(), object))
		panic(err)
	}
	return nil
}

func deployOrWipeTestingInfrastrucure(actionOnObject func(unstructured.Unstructured) error) {
	// Deploy / delete test infrastructure / dependencies
	manifests := GetListOfManifests(flags.PathToTestingInfrastrucureManifests)
	for _, manifest := range manifests {
		objects := ReadManifestYamlFile(manifest)
		for _, obj := range objects {
			err := actionOnObject(obj)
			util2.PanicOnError(err)
		}
	}

	WaitForAllPodsReady(3*time.Minute, metav1.ListOptions{})
}

func DeployTestingInfrastructure() {
	deployOrWipeTestingInfrastrucure(ApplyRawManifest)
}

func WipeTestingInfrastructure() {
	deployOrWipeTestingInfrastrucure(DeleteRawManifest)
}

func cleanupSubresourceServiceAccount() {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	err = virtCli.CoreV1().ServiceAccounts(util2.NamespaceTestDefault).Delete(context.Background(), SubresourceServiceAccountName, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		util2.PanicOnError(err)
	}

	err = virtCli.RbacV1().Roles(util2.NamespaceTestDefault).Delete(context.Background(), SubresourceServiceAccountName, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		util2.PanicOnError(err)
	}

	err = virtCli.RbacV1().RoleBindings(util2.NamespaceTestDefault).Delete(context.Background(), SubresourceServiceAccountName, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		util2.PanicOnError(err)
	}
}

func createServiceAccount(saName string, clusterRole string) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	sa := k8sv1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: util2.NamespaceTestDefault,
			Labels: map[string]string{
				"kubevirt.io/test": saName,
			},
		},
	}

	_, err = virtCli.CoreV1().ServiceAccounts(util2.NamespaceTestDefault).Create(context.Background(), &sa, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}

	roleBinding := rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: util2.NamespaceTestDefault,
			Labels: map[string]string{
				"kubevirt.io/test": saName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     clusterRole,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}
	roleBinding.Subjects = append(roleBinding.Subjects, rbacv1.Subject{
		Kind:      "ServiceAccount",
		Name:      saName,
		Namespace: util2.NamespaceTestDefault,
	})

	_, err = virtCli.RbacV1().RoleBindings(util2.NamespaceTestDefault).Create(context.Background(), &roleBinding, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}
}

func cleanupServiceAccount(saName string) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	err = virtCli.RbacV1().RoleBindings(util2.NamespaceTestDefault).Delete(context.Background(), saName, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		util2.PanicOnError(err)
	}

	err = virtCli.CoreV1().ServiceAccounts(util2.NamespaceTestDefault).Delete(context.Background(), saName, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		util2.PanicOnError(err)
	}
}

func createSubresourceServiceAccount() {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	sa := k8sv1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SubresourceServiceAccountName,
			Namespace: util2.NamespaceTestDefault,
			Labels: map[string]string{
				"kubevirt.io/test": "sa",
			},
		},
	}

	_, err = virtCli.CoreV1().ServiceAccounts(util2.NamespaceTestDefault).Create(context.Background(), &sa, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}

	role := rbacv1.Role{

		ObjectMeta: metav1.ObjectMeta{
			Name:      SubresourceServiceAccountName,
			Namespace: util2.NamespaceTestDefault,
			Labels: map[string]string{
				"kubevirt.io/test": "sa",
			},
		},
	}
	role.Rules = append(role.Rules, rbacv1.PolicyRule{
		APIGroups: []string{"subresources.kubevirt.io"},
		Resources: []string{"virtualmachines/start"},
		Verbs:     []string{"update"},
	})

	_, err = virtCli.RbacV1().Roles(util2.NamespaceTestDefault).Create(context.Background(), &role, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}

	roleBinding := rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SubresourceServiceAccountName,
			Namespace: util2.NamespaceTestDefault,
			Labels: map[string]string{
				"kubevirt.io/test": "sa",
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     SubresourceServiceAccountName,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}
	roleBinding.Subjects = append(roleBinding.Subjects, rbacv1.Subject{
		Kind:      "ServiceAccount",
		Name:      SubresourceServiceAccountName,
		Namespace: util2.NamespaceTestDefault,
	})

	_, err = virtCli.RbacV1().RoleBindings(util2.NamespaceTestDefault).Create(context.Background(), &roleBinding, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}
}

func createServiceAccounts() {
	createSubresourceServiceAccount()

	createServiceAccount(AdminServiceAccountName, "kubevirt.io:admin")
	createServiceAccount(ViewServiceAccountName, "kubevirt.io:view")
	createServiceAccount(EditServiceAccountName, "kubevirt.io:edit")
}

func cleanupServiceAccounts() {
	cleanupSubresourceServiceAccount()

	cleanupServiceAccount(AdminServiceAccountName)
	cleanupServiceAccount(ViewServiceAccountName)
	cleanupServiceAccount(EditServiceAccountName)
}

func DeleteConfigMap(name string) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	err = virtCli.CoreV1().ConfigMaps(util2.NamespaceTestDefault).Delete(context.Background(), name, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		util2.PanicOnError(err)
	}
}

func DeleteSecret(name string) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	err = virtCli.CoreV1().Secrets(util2.NamespaceTestDefault).Delete(context.Background(), name, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		util2.PanicOnError(err)
	}
}

func DeletePVC(os string) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	name := fmt.Sprintf("disk-%s", os)
	err = virtCli.CoreV1().PersistentVolumeClaims((util2.NamespaceTestDefault)).Delete(context.Background(), name, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		util2.PanicOnError(err)
	}
}

func DeletePV(os string) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	name := fmt.Sprintf("%s-disk-for-tests", os)
	err = virtCli.CoreV1().PersistentVolumes().Delete(context.Background(), name, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		util2.PanicOnError(err)
	}
}

func VerifyDummyNicForBridgeNetwork(vmi *v1.VirtualMachineInstance) {
	output := RunCommandOnVmiPod(vmi, []string{"/bin/bash", "-c", "/usr/sbin/ip link show|grep DOWN|grep -c eth0"})
	ExpectWithOffset(1, strings.TrimSpace(output)).To(Equal("1"))

	output = RunCommandOnVmiPod(vmi, []string{"/bin/bash", "-c", "/usr/sbin/ip link show|grep UP|grep -c eth0-nic"})
	ExpectWithOffset(1, strings.TrimSpace(output)).To(Equal("1"))

	return
}

func RunVMI(vmi *v1.VirtualMachineInstance, timeout int) *v1.VirtualMachineInstance {
	By("Starting a VirtualMachineInstance")
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	var obj *v1.VirtualMachineInstance
	Eventually(func() error {
		obj, err = virtCli.VirtualMachineInstance(util2.NamespaceTestDefault).Create(vmi)
		return err
	}, timeout, 1*time.Second).ShouldNot(HaveOccurred())
	return obj
}

func RunVMIAndExpectLaunch(vmi *v1.VirtualMachineInstance, timeout int) *v1.VirtualMachineInstance {
	obj := RunVMI(vmi, timeout)
	By("Waiting until the VirtualMachineInstance will start")
	WaitForSuccessfulVMIStartWithTimeout(obj, timeout)
	return obj
}

func RunVMIAndExpectLaunchWithDataVolume(vmi *v1.VirtualMachineInstance, dv *cdiv1.DataVolume, timeout int) *v1.VirtualMachineInstance {
	obj := RunVMI(vmi, timeout)
	By("Waiting until the DataVolume is ready")
	WaitForSuccessfulDataVolumeImport(dv, timeout)
	By("Waiting until the VirtualMachineInstance will start")
	WaitForSuccessfulVMIStartWithTimeout(obj, timeout)
	return obj
}

func RunVMIAndExpectLaunchIgnoreWarnings(vmi *v1.VirtualMachineInstance, timeout int) *v1.VirtualMachineInstance {
	obj := RunVMI(vmi, timeout)
	By("Waiting until the VirtualMachineInstance will start")
	WaitForSuccessfulVMIStartWithTimeoutIgnoreWarnings(obj, timeout)
	return obj
}

func RunVMIAndExpectLaunchWithIgnoreWarningArg(vmi *v1.VirtualMachineInstance, timeout int, ignoreWarnings bool) *v1.VirtualMachineInstance {
	By("Starting a VirtualMachineInstance")
	var obj *v1.VirtualMachineInstance
	var err error
	virtClient, err := kubecli.GetKubevirtClient()
	Expect(err).ToNot(HaveOccurred())
	Eventually(func() error {
		obj, err = virtClient.VirtualMachineInstance(util2.NamespaceTestDefault).Create(vmi)
		return err
	}, timeout, 1*time.Second).ShouldNot(HaveOccurred())
	By("Waiting until the VirtualMachineInstance starts")
	if ignoreWarnings {
		WaitForSuccessfulVMIStartWithTimeoutIgnoreWarnings(obj, timeout)
	} else {
		WaitForSuccessfulVMIStartWithTimeout(obj, timeout)
	}
	vmi, err = virtClient.VirtualMachineInstance(util2.NamespaceTestDefault).Get(vmi.Name, &metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred())
	return vmi
}

func RunVMIAndExpectScheduling(vmi *v1.VirtualMachineInstance, timeout int) *v1.VirtualMachineInstance {
	obj := RunVMI(vmi, timeout)
	By("Waiting until the VirtualMachineInstance will be scheduled")
	wp := WarningsPolicy{FailOnWarnings: true}
	waitForVMIScheduling(obj, timeout, wp)
	return obj
}

func getRunningPodByVirtualMachineInstance(vmi *v1.VirtualMachineInstance, namespace string) (*k8sv1.Pod, error) {
	virtCli, err := kubecli.GetKubevirtClient()
	if err != nil {
		return nil, err
	}

	vmi, err = virtCli.VirtualMachineInstance(namespace).Get(vmi.Name, &metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return GetRunningPodByLabel(string(vmi.GetUID()), v1.CreatedByLabel, namespace, vmi.Status.NodeName)
}

func GetRunningPodByVirtualMachineInstance(vmi *v1.VirtualMachineInstance, namespace string) *k8sv1.Pod {
	pod, err := getRunningPodByVirtualMachineInstance(vmi, namespace)
	util2.PanicOnError(err)
	return pod
}

func GetPodByVirtualMachineInstance(vmi *v1.VirtualMachineInstance) *k8sv1.Pod {
	pods, err := getPodsByLabel(string(vmi.GetUID()), v1.CreatedByLabel, vmi.Namespace)
	util2.PanicOnError(err)

	if len(pods.Items) != 1 {
		util2.PanicOnError(fmt.Errorf("found wrong number of pods for VMI '%v', count: %d", vmi, len(pods.Items)))
	}

	return &pods.Items[0]
}

func getPodsByLabel(label, labelType, namespace string) (*k8sv1.PodList, error) {
	virtCli, err := kubecli.GetKubevirtClient()
	if err != nil {
		return nil, err
	}

	labelSelector := fmt.Sprintf("%s=%s", labelType, label)

	pods, err := virtCli.CoreV1().Pods(namespace).List(context.Background(),
		metav1.ListOptions{LabelSelector: labelSelector},
	)
	if err != nil {
		return nil, err
	}

	return pods, nil
}

func GetRunningPodByLabel(label string, labelType string, namespace string, node string) (*k8sv1.Pod, error) {
	virtCli, err := kubecli.GetKubevirtClient()
	if err != nil {
		return nil, err
	}

	labelSelector := fmt.Sprintf("%s=%s", labelType, label)
	var fieldSelector string
	if node != "" {
		fieldSelector = fmt.Sprintf("status.phase==%s,spec.nodeName==%s", k8sv1.PodRunning, node)
	} else {
		fieldSelector = fmt.Sprintf("status.phase==%s", k8sv1.PodRunning)
	}
	pods, err := virtCli.CoreV1().Pods(namespace).List(context.Background(),
		metav1.ListOptions{LabelSelector: labelSelector, FieldSelector: fieldSelector},
	)
	if err != nil {
		return nil, err
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("failed to find pod with the label %s", label)
	}

	var readyPod *k8sv1.Pod
	for _, pod := range pods.Items {
		ready := true
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "kubevirt-infra" {
				ready = status.Ready
				break
			}
		}
		if ready {
			readyPod = &pod
			break
		}
	}
	if readyPod == nil {
		return nil, fmt.Errorf("no ready pods with the label %s", label)
	}

	return readyPod, nil
}

func GetComputeContainerOfPod(pod *k8sv1.Pod) *k8sv1.Container {
	return GetContainerOfPod(pod, "compute")
}

func GetContainerDiskContainerOfPod(pod *k8sv1.Pod, volumeName string) *k8sv1.Container {
	diskContainerName := fmt.Sprintf("volume%s", volumeName)
	return GetContainerOfPod(pod, diskContainerName)
}

func GetContainerOfPod(pod *k8sv1.Pod, containerName string) *k8sv1.Container {
	var computeContainer *k8sv1.Container
	for _, container := range pod.Spec.Containers {
		if container.Name == containerName {
			computeContainer = &container
			break
		}
	}
	if computeContainer == nil {
		util2.PanicOnError(fmt.Errorf("could not find the %s container", containerName))
	}
	return computeContainer
}

func cleanNamespaces() {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	for _, namespace := range TestNamespaces {

		_, err := virtCli.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
		if err != nil {
			continue
		}

		// Clean namespace labels
		err = libnet.RemoveAllLabelsFromNamespace(virtCli, namespace)
		util2.PanicOnError(err)

		//Remove all Jobs
		util2.PanicOnError(virtCli.BatchV1().RESTClient().Delete().Namespace(namespace).Resource("jobs").Do(context.Background()).Error())
		//Remove all HPA
		util2.PanicOnError(virtCli.AutoscalingV1().RESTClient().Delete().Namespace(namespace).Resource("horizontalpodautoscalers").Do(context.Background()).Error())

		// Remove all VirtualMachines
		util2.PanicOnError(virtCli.RestClient().Delete().Namespace(namespace).Resource("virtualmachines").Do(context.Background()).Error())

		// Remove all VirtualMachineReplicaSets
		util2.PanicOnError(virtCli.RestClient().Delete().Namespace(namespace).Resource("virtualmachineinstancereplicasets").Do(context.Background()).Error())

		// Remove all VMIs
		util2.PanicOnError(virtCli.RestClient().Delete().Namespace(namespace).Resource("virtualmachineinstances").Do(context.Background()).Error())
		vmis, err := virtCli.VirtualMachineInstance(namespace).List(&metav1.ListOptions{})
		util2.PanicOnError(err)
		for _, vmi := range vmis.Items {
			if controller.HasFinalizer(&vmi, v1.VirtualMachineInstanceFinalizer) {
				_, err := virtCli.VirtualMachineInstance(vmi.Namespace).Patch(vmi.Name, types.JSONPatchType, []byte("[{ \"op\": \"remove\", \"path\": \"/metadata/finalizers\" }]"))
				if !errors.IsNotFound(err) {
					util2.PanicOnError(err)
				}
			}
		}

		// Remove all Pods
		podList, err := virtCli.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
		util2.PanicOnError(err)
		var gracePeriod int64 = 0
		for _, pod := range podList.Items {
			err := virtCli.CoreV1().Pods(namespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &gracePeriod})
			if errors.IsNotFound(err) {
				continue
			}
			Expect(err).ToNot(HaveOccurred())
		}

		// Remove all Services
		svcList, err := virtCli.CoreV1().Services(namespace).List(context.Background(), metav1.ListOptions{})
		util2.PanicOnError(err)
		for _, svc := range svcList.Items {
			util2.PanicOnError(virtCli.CoreV1().Services(namespace).Delete(context.Background(), svc.Name, metav1.DeleteOptions{}))
		}

		// Remove PVCs
		util2.PanicOnError(virtCli.CoreV1().RESTClient().Delete().Namespace(namespace).Resource("persistentvolumeclaims").Do(context.Background()).Error())
		if HasCDI() {
			// Remove DataVolumes
			util2.PanicOnError(virtCli.CdiClient().CdiV1beta1().RESTClient().Delete().Namespace(namespace).Resource("datavolumes").Do(context.Background()).Error())
		}
		// Remove PVs
		pvs, err := virtCli.CoreV1().PersistentVolumes().List(context.Background(), metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s", cleanup.TestLabelForNamespace(namespace)),
		})
		util2.PanicOnError(err)
		for _, pv := range pvs.Items {
			err := virtCli.CoreV1().PersistentVolumes().Delete(context.Background(), pv.Name, metav1.DeleteOptions{})
			if err != nil && !errors.IsNotFound(err) {
				util2.PanicOnError(err)
			}
		}

		// Remove all VirtualMachineInstance Secrets
		labelSelector := fmt.Sprintf("%s", SecretLabel)
		util2.PanicOnError(
			virtCli.CoreV1().Secrets(namespace).DeleteCollection(context.Background(),
				metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: labelSelector},
			),
		)

		// Remove all VirtualMachineInstance Presets
		util2.PanicOnError(virtCli.RestClient().Delete().Namespace(namespace).Resource("virtualmachineinstancepresets").Do(context.Background()).Error())
		// Remove all limit ranges
		util2.PanicOnError(virtCli.CoreV1().RESTClient().Delete().Namespace(namespace).Resource("limitranges").Do(context.Background()).Error())

		// Remove all Migration Objects
		util2.PanicOnError(virtCli.RestClient().Delete().Namespace(namespace).Resource("virtualmachineinstancemigrations").Do(context.Background()).Error())
		migrations, err := virtCli.VirtualMachineInstanceMigration(namespace).List(&metav1.ListOptions{})
		util2.PanicOnError(err)
		for _, migration := range migrations.Items {
			if controller.HasFinalizer(&migration, v1.VirtualMachineInstanceMigrationFinalizer) {
				_, err := virtCli.VirtualMachineInstanceMigration(namespace).Patch(migration.Name, types.JSONPatchType, []byte("[{ \"op\": \"remove\", \"path\": \"/metadata/finalizers\" }]"))
				if !errors.IsNotFound(err) {
					util2.PanicOnError(err)
				}
			}
		}
		// Remove all NetworkAttachmentDefinitions
		nets, err := virtCli.NetworkClient().K8sCniCncfIoV1().NetworkAttachmentDefinitions(namespace).List(context.Background(), metav1.ListOptions{})
		if err != nil && !errors.IsNotFound(err) {
			util2.PanicOnError(err)
		}
		for _, netDef := range nets.Items {
			util2.PanicOnError(virtCli.NetworkClient().K8sCniCncfIoV1().NetworkAttachmentDefinitions(namespace).Delete(context.Background(), netDef.GetName(), metav1.DeleteOptions{}))
		}

		// Remove all Istio Sidecars, VirtualServices, DestinationRules and Gateways
		for _, res := range []string{"sidecars", "virtualservices", "destinationrules", "gateways"} {
			util2.PanicOnError(removeAllGroupVersionResourceFromNamespace(schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1beta1", Resource: res}, namespace))
		}

		// Remove all Istio PeerAuthentications
		util2.PanicOnError(removeAllGroupVersionResourceFromNamespace(schema.GroupVersionResource{Group: "security.istio.io", Version: "v1beta1", Resource: "peerauthentications"}, namespace))
	}
}

func removeNamespaces() {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	// First send an initial delete to every namespace
	for _, namespace := range TestNamespaces {
		err := virtCli.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		if !errors.IsNotFound(err) {
			util2.PanicOnError(err)
		}
	}

	// Wait until the namespaces are terminated
	fmt.Println("")
	for _, namespace := range TestNamespaces {
		fmt.Printf("Waiting for namespace %s to be removed, this can take a while ...\n", namespace)
		EventuallyWithOffset(1, func() error {
			return virtCli.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		}, 240*time.Second, 1*time.Second).Should(SatisfyAll(HaveOccurred(), WithTransform(errors.IsNotFound, BeTrue())), fmt.Sprintf("should successfully delete namespace '%s'", namespace))
	}
}

func removeAllGroupVersionResourceFromNamespace(groupVersionResource schema.GroupVersionResource, namespace string) error {
	virtCli, err := kubecli.GetKubevirtClient()
	if err != nil {
		return err
	}

	gvr, err := virtCli.DynamicClient().Resource(groupVersionResource).Namespace(namespace).List(context.Background(), metav1.ListOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	for _, r := range gvr.Items {
		err = virtCli.DynamicClient().Resource(groupVersionResource).Namespace(namespace).Delete(context.Background(), r.GetName(), metav1.DeleteOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}

func detectInstallNamespace() {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)
	kvs, err := virtCli.KubeVirt("").List(&metav1.ListOptions{})
	util2.PanicOnError(err)
	if len(kvs.Items) == 0 {
		util2.PanicOnError(fmt.Errorf("Could not detect a kubevirt installation"))
	}
	if len(kvs.Items) > 1 {
		util2.PanicOnError(fmt.Errorf("Invalid kubevirt installation, more than one KubeVirt resource found"))
	}
	flags.KubeVirtInstallNamespace = kvs.Items[0].Namespace
}

func createNamespaces() {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	// Create a Test Namespaces
	for _, namespace := range TestNamespaces {
		ns := &k8sv1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		_, err = virtCli.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
		if !errors.IsAlreadyExists(err) {
			util2.PanicOnError(err)
		}
	}
}

func NewRandomDataVolumeWithRegistryImport(imageUrl, namespace string, accessMode k8sv1.PersistentVolumeAccessMode) *cdiv1.DataVolume {
	return NewRandomDataVolumeWithRegistryImportInStorageClass(imageUrl, namespace, Config.StorageClassLocal, accessMode)
}

func NewRandomDataVolumeWithHttpImport(imageUrl, namespace string, accessMode k8sv1.PersistentVolumeAccessMode) *cdiv1.DataVolume {
	return newRandomDataVolumeWithHttpImport(imageUrl, namespace, Config.StorageClassLocal, accessMode)
}

func NewRandomDataVolumeWithHttpImportInStorageClass(imageUrl, namespace, storageClass string, accessMode k8sv1.PersistentVolumeAccessMode) *cdiv1.DataVolume {
	return newRandomDataVolumeWithHttpImport(imageUrl, namespace, storageClass, accessMode)
}

func NewRandomBlankDataVolume(namespace, storageClass, size string, accessMode k8sv1.PersistentVolumeAccessMode, volumeMode k8sv1.PersistentVolumeMode) *cdiv1.DataVolume {
	return newRandomBlankDataVolume(namespace, storageClass, size, accessMode, volumeMode)
}

func NewRandomVirtualMachineInstanceWithOCSDisk(imageUrl, namespace string, accessMode k8sv1.PersistentVolumeAccessMode, volMode k8sv1.PersistentVolumeMode) (*v1.VirtualMachineInstance, *cdiv1.DataVolume) {
	if !HasCDI() {
		Skip("Skip DataVolume tests when CDI is not present")
	}
	sc, exists := GetCephStorageClass()
	if !exists {
		Skip("Skip OCS tests when Ceph is not present")
	}
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	dv := newRandomDataVolumeWithHttpImport(imageUrl, namespace, sc, accessMode)
	dv.Spec.PVC.VolumeMode = &volMode
	_, err = virtCli.CdiClient().CdiV1beta1().DataVolumes(dv.Namespace).Create(context.Background(), dv, metav1.CreateOptions{})
	Expect(err).ToNot(HaveOccurred())
	WaitForSuccessfulDataVolumeImport(dv, 240)
	return NewRandomVMIWithDataVolume(dv.Name), dv
}

func NewRandomDataVolumeWithRegistryImportInStorageClass(imageUrl, namespace, storageClass string, accessMode k8sv1.PersistentVolumeAccessMode) *cdiv1.DataVolume {
	name := "test-datavolume-" + rand.String(12)
	quantity, err := resource.ParseQuantity("1Gi")
	util2.PanicOnError(err)
	dataVolume := &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: &cdiv1.DataVolumeSource{
				Registry: &cdiv1.DataVolumeSourceRegistry{
					URL: imageUrl,
				},
			},
			PVC: &k8sv1.PersistentVolumeClaimSpec{
				AccessModes: []k8sv1.PersistentVolumeAccessMode{accessMode},
				Resources: k8sv1.ResourceRequirements{
					Requests: k8sv1.ResourceList{
						"storage": quantity,
					},
				},
				StorageClassName: &storageClass,
			},
		},
	}

	dataVolume.TypeMeta = metav1.TypeMeta{
		APIVersion: "cdi.kubevirt.io/v1alpha1",
		Kind:       "DataVolume",
	}

	return dataVolume
}

func newRandomDataVolumeWithHttpImport(imageUrl, namespace, storageClass string, accessMode k8sv1.PersistentVolumeAccessMode) *cdiv1.DataVolume {
	name := "test-datavolume-" + rand.String(12)
	quantity, err := resource.ParseQuantity("1Gi")
	util2.PanicOnError(err)
	dataVolume := &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: &cdiv1.DataVolumeSource{
				HTTP: &cdiv1.DataVolumeSourceHTTP{
					URL: imageUrl,
				},
			},
			PVC: &k8sv1.PersistentVolumeClaimSpec{
				AccessModes: []k8sv1.PersistentVolumeAccessMode{accessMode},
				Resources: k8sv1.ResourceRequirements{
					Requests: k8sv1.ResourceList{
						"storage": quantity,
					},
				},
				StorageClassName: &storageClass,
			},
		},
	}

	dataVolume.TypeMeta = metav1.TypeMeta{
		APIVersion: "cdi.kubevirt.io/v1alpha1",
		Kind:       "DataVolume",
	}

	return dataVolume
}

func newRandomBlankDataVolume(namespace, storageClass, size string, accessMode k8sv1.PersistentVolumeAccessMode, volumeMode k8sv1.PersistentVolumeMode) *cdiv1.DataVolume {
	name := "test-datavolume-" + rand.String(12)
	quantity, err := resource.ParseQuantity(size)
	util2.PanicOnError(err)
	dataVolume := &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: &cdiv1.DataVolumeSource{
				Blank: &cdiv1.DataVolumeBlankImage{},
			},
			PVC: &k8sv1.PersistentVolumeClaimSpec{
				AccessModes: []k8sv1.PersistentVolumeAccessMode{accessMode},
				Resources: k8sv1.ResourceRequirements{
					Requests: k8sv1.ResourceList{
						"storage": quantity,
					},
				},
				StorageClassName: &storageClass,
				VolumeMode:       &volumeMode,
			},
		},
	}

	dataVolume.TypeMeta = metav1.TypeMeta{
		APIVersion: "cdi.kubevirt.io/v1alpha1",
		Kind:       "DataVolume",
	}

	return dataVolume
}

func NewRandomDataVolumeWithPVCSource(sourceNamespace, sourceName, targetNamespace string, accessMode k8sv1.PersistentVolumeAccessMode) *cdiv1.DataVolume {
	return NewRandomDataVolumeWithPVCSourceWithStorageClass(sourceNamespace, sourceName, targetNamespace, Config.StorageClassLocal, "1Gi", accessMode)
}

func NewRandomDataVolumeWithPVCSourceWithStorageClass(sourceNamespace, sourceName, targetNamespace, storageClass, size string, accessMode k8sv1.PersistentVolumeAccessMode) *cdiv1.DataVolume {
	name := "test-datavolume-" + rand.String(12)
	quantity, err := resource.ParseQuantity(size)
	util2.PanicOnError(err)
	dataVolume := &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: targetNamespace,
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: &cdiv1.DataVolumeSource{
				PVC: &cdiv1.DataVolumeSourcePVC{
					Namespace: sourceNamespace,
					Name:      sourceName,
				},
			},
			PVC: &k8sv1.PersistentVolumeClaimSpec{
				AccessModes: []k8sv1.PersistentVolumeAccessMode{accessMode},
				Resources: k8sv1.ResourceRequirements{
					Requests: k8sv1.ResourceList{
						"storage": quantity,
					},
				},
				StorageClassName: &storageClass,
			},
		},
	}

	dataVolume.TypeMeta = metav1.TypeMeta{
		APIVersion: "cdi.kubevirt.io/v1alpha1",
		Kind:       "DataVolume",
	}

	return dataVolume
}

func NewRandomVMI() *v1.VirtualMachineInstance {
	return NewRandomVMIWithNS(util2.NamespaceTestDefault)
}

func NewRandomVMIWithNS(namespace string) *v1.VirtualMachineInstance {
	vmi := v1.NewMinimalVMIWithNS(namespace, libvmi.RandName(libvmi.DefaultVmiName))

	t := defaultTestGracePeriod
	vmi.Spec.TerminationGracePeriodSeconds = &t

	// To avoid mac address issue in the tests change the pod interface binding to masquerade
	// https://github.com/kubevirt/kubevirt/issues/1494
	vmi.Spec.Domain.Devices = v1.Devices{Interfaces: []v1.Interface{{Name: "default",
		InterfaceBindingMethod: v1.InterfaceBindingMethod{
			Masquerade: &v1.InterfaceMasquerade{}}}}}

	vmi.Spec.Networks = []v1.Network{*v1.DefaultPodNetwork()}
	vmi.Spec.Domain.Resources.Requests[k8sv1.ResourceMemory] = resource.MustParse("128Mi")

	return vmi
}

func NewRandomVMIWithDataVolume(dataVolumeName string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMI()

	diskName := "disk0"
	bus := "virtio"
	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: diskName,
		DiskDevice: v1.DiskDevice{
			Disk: &v1.DiskTarget{
				Bus: bus,
			},
		},
	})
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: diskName,
		VolumeSource: v1.VolumeSource{
			DataVolume: &v1.DataVolumeSource{
				Name: dataVolumeName,
			},
		},
	})

	vmi.Spec.Domain.Resources.Requests[k8sv1.ResourceMemory] = resource.MustParse("1Gi")
	return vmi
}

func NewRandomVMWithEphemeralDisk(containerImage string) *v1.VirtualMachine {
	vmi := NewRandomVMIWithEphemeralDisk(containerImage)
	vm := NewRandomVirtualMachine(vmi, false)

	return vm
}

func addDataVolumeTemplate(vm *v1.VirtualMachine, dataVolume *cdiv1.DataVolume) {
	dvt := &v1.DataVolumeTemplateSpec{}

	dvt.Spec = *dataVolume.Spec.DeepCopy()
	dvt.ObjectMeta = *dataVolume.ObjectMeta.DeepCopy()

	vm.Spec.DataVolumeTemplates = append(vm.Spec.DataVolumeTemplates, *dvt)
}

func NewRandomVMWithRegistryDataVolume(imageUrl string, namespace string) *v1.VirtualMachine {
	dataVolume := NewRandomDataVolumeWithRegistryImport(imageUrl, namespace, k8sv1.ReadWriteOnce)
	vmi := NewRandomVMIWithDataVolume(dataVolume.Name)
	vm := NewRandomVirtualMachine(vmi, false)

	addDataVolumeTemplate(vm, dataVolume)
	return vm
}

func NewRandomVMWithDataVolume(imageUrl string, namespace string) *v1.VirtualMachine {
	dataVolume := NewRandomDataVolumeWithHttpImport(imageUrl, namespace, k8sv1.ReadWriteOnce)
	vmi := NewRandomVMIWithDataVolume(dataVolume.Name)
	vm := NewRandomVirtualMachine(vmi, false)

	addDataVolumeTemplate(vm, dataVolume)
	return vm
}

func NewRandomVMWithDataVolumeInStorageClass(imageUrl, namespace, storageClass string) *v1.VirtualMachine {
	dataVolume := NewRandomDataVolumeWithHttpImportInStorageClass(imageUrl, namespace, storageClass, k8sv1.ReadWriteOnce)
	vmi := NewRandomVMIWithDataVolume(dataVolume.Name)
	vm := NewRandomVirtualMachine(vmi, false)

	addDataVolumeTemplate(vm, dataVolume)
	return vm
}

func NewRandomVMWithDataVolumeAndUserData(dataVolume *cdiv1.DataVolume, userData string) *v1.VirtualMachine {
	vmi := NewRandomVMIWithDataVolume(dataVolume.Name)
	AddUserData(vmi, "cloud-init", userData)
	vm := NewRandomVirtualMachine(vmi, false)

	addDataVolumeTemplate(vm, dataVolume)
	return vm
}

func NewRandomVMWithDataVolumeAndUserDataInStorageClass(imageUrl, namespace, userData, storageClass string) *v1.VirtualMachine {
	dataVolume := NewRandomDataVolumeWithHttpImportInStorageClass(imageUrl, namespace, storageClass, k8sv1.ReadWriteOnce)
	return NewRandomVMWithDataVolumeAndUserData(dataVolume, userData)
}

func NewRandomVMWithCloneDataVolume(sourceNamespace, sourceName, targetNamespace string) *v1.VirtualMachine {
	dataVolume := NewRandomDataVolumeWithPVCSource(sourceNamespace, sourceName, targetNamespace, k8sv1.ReadWriteOnce)
	vmi := NewRandomVMIWithDataVolume(dataVolume.Name)
	vmi.Namespace = targetNamespace
	vm := NewRandomVirtualMachine(vmi, false)

	addDataVolumeTemplate(vm, dataVolume)
	return vm
}

func NewRandomVMIWithEphemeralDiskHighMemory(containerImage string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDisk(containerImage)

	vmi.Spec.Domain.Resources.Requests[k8sv1.ResourceMemory] = resource.MustParse("512M")
	return vmi
}

func NewRandomVMIWithEphemeralDiskAndUserdataHighMemory(containerImage string, userData string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDiskAndUserdata(containerImage, userData)

	vmi.Spec.Domain.Resources.Requests[k8sv1.ResourceMemory] = resource.MustParse("512M")
	return vmi
}

func NewRandomVMIWithEphemeralDiskAndConfigDriveUserdataHighMemory(containerImage string, userData string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDiskAndConfigDriveUserdata(containerImage, userData)

	vmi.Spec.Domain.Resources.Requests[k8sv1.ResourceMemory] = resource.MustParse("512M")
	return vmi
}

func NewRandomVMIWithEFIBootloader() *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDiskHighMemory(cd.ContainerDiskFor(cd.ContainerDiskAlpine))

	// EFI needs more memory than other images
	vmi.Spec.Domain.Resources.Requests[k8sv1.ResourceMemory] = resource.MustParse("1Gi")
	vmi.Spec.Domain.Firmware = &v1.Firmware{
		Bootloader: &v1.Bootloader{
			EFI: &v1.EFI{
				SecureBoot: NewBool(false),
			},
		},
	}

	return vmi

}

func NewRandomVMIWithSecureBoot() *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDiskHighMemory(cd.ContainerDiskFor(cd.ContainerDiskMicroLiveCD))

	// EFI needs more memory than other images
	vmi.Spec.Domain.Resources.Requests[k8sv1.ResourceMemory] = resource.MustParse("1Gi")
	vmi.Spec.Domain.Features = &v1.Features{
		SMM: &v1.FeatureState{
			Enabled: NewBool(true),
		},
	}
	vmi.Spec.Domain.Firmware = &v1.Firmware{
		Bootloader: &v1.Bootloader{
			EFI: &v1.EFI{}, // SecureBoot should default to true
		},
	}

	return vmi

}

func NewRandomMigration(vmiName string, namespace string) *v1.VirtualMachineInstanceMigration {
	return &v1.VirtualMachineInstanceMigration{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1.GroupVersion.String(),
			Kind:       "VirtualMachineInstanceMigration",
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "test-migration-",
			Namespace:    namespace,
		},
		Spec: v1.VirtualMachineInstanceMigrationSpec{
			VMIName: vmiName,
		},
	}
}

func NewRandomVMIWithEphemeralDisk(containerImage string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMI()

	AddEphemeralDisk(vmi, "disk0", "virtio", containerImage)
	if containerImage == cd.ContainerDiskFor(cd.ContainerDiskFedoraTestTooling) {
		vmi.Spec.Domain.Devices.Rng = &v1.Rng{} // newer fedora kernels may require hardware RNG to boot
	}
	return vmi
}

func AddEphemeralDisk(vmi *v1.VirtualMachineInstance, name string, bus string, image string) *v1.VirtualMachineInstance {
	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: name,
		DiskDevice: v1.DiskDevice{
			Disk: &v1.DiskTarget{
				Bus: bus,
			},
		},
	})
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: name,
		VolumeSource: v1.VolumeSource{
			ContainerDisk: &v1.ContainerDiskSource{
				Image: image,
			},
		},
	})

	return vmi
}

func AddBootOrderToDisk(vmi *v1.VirtualMachineInstance, diskName string, bootorder *uint) *v1.VirtualMachineInstance {
	for i, d := range vmi.Spec.Domain.Devices.Disks {
		if d.Name == diskName {
			vmi.Spec.Domain.Devices.Disks[i].BootOrder = bootorder
			return vmi
		}
	}
	return vmi
}

func AddPVCDisk(vmi *v1.VirtualMachineInstance, name string, bus string, claimName string) *v1.VirtualMachineInstance {
	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: name,
		DiskDevice: v1.DiskDevice{
			Disk: &v1.DiskTarget{
				Bus: bus,
			},
		},
	})
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: name,
		VolumeSource: v1.VolumeSource{
			PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{
				ClaimName: claimName,
			},
		},
	})

	return vmi
}

func AddEphemeralCdrom(vmi *v1.VirtualMachineInstance, name string, bus string, image string) *v1.VirtualMachineInstance {
	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: name,
		DiskDevice: v1.DiskDevice{
			CDRom: &v1.CDRomTarget{
				Bus: bus,
			},
		},
	})
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: name,
		VolumeSource: v1.VolumeSource{
			ContainerDisk: &v1.ContainerDiskSource{
				Image: image,
			},
		},
	})

	return vmi
}

func NewRandomFedoraVMI() *v1.VirtualMachineInstance {
	networkData, err := libnet.CreateDefaultCloudInitNetworkData()
	Expect(err).NotTo(HaveOccurred())

	return libvmi.NewFedora(
		libvmi.WithInterface(libvmi.InterfaceDeviceWithMasqueradeBinding()),
		libvmi.WithNetwork(v1.DefaultPodNetwork()),
		libvmi.WithCloudInitNoCloudNetworkData(networkData, false),
	)
}

func NewRandomFedoraVMIWithGuestAgent() *v1.VirtualMachineInstance {
	networkData, err := libnet.CreateDefaultCloudInitNetworkData()
	Expect(err).NotTo(HaveOccurred())

	return libvmi.NewTestToolingFedora(
		libvmi.WithInterface(libvmi.InterfaceDeviceWithMasqueradeBinding()),
		libvmi.WithNetwork(v1.DefaultPodNetwork()),
		libvmi.WithCloudInitNoCloudNetworkData(networkData, false),
	)
}

func NewRandomFedoraVMIWithBlacklistGuestAgent(commands string) *v1.VirtualMachineInstance {
	networkData, err := libnet.CreateDefaultCloudInitNetworkData()
	Expect(err).NotTo(HaveOccurred())

	return libvmi.NewTestToolingFedora(
		libvmi.WithInterface(libvmi.InterfaceDeviceWithMasqueradeBinding()),
		libvmi.WithNetwork(v1.DefaultPodNetwork()),
		libvmi.WithCloudInitNoCloudUserData(GetFedoraToolsGuestAgentBlacklistUserData(commands), false),
		libvmi.WithCloudInitNoCloudNetworkData(networkData, false),
	)
}

func AddPVCFS(vmi *v1.VirtualMachineInstance, name string, claimName string) *v1.VirtualMachineInstance {
	vmi.Spec.Domain.Devices.Filesystems = append(vmi.Spec.Domain.Devices.Filesystems, v1.Filesystem{
		Name:     name,
		Virtiofs: &v1.FilesystemVirtiofs{},
	})
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: name,
		VolumeSource: v1.VolumeSource{
			PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{
				ClaimName: claimName,
			},
		},
	})

	return vmi
}

func NewRandomVMIWithFSFromDataVolume(dataVolumeName string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMI()
	containerImage := cd.ContainerDiskFor(cd.ContainerDiskFedoraTestTooling)
	AddEphemeralDisk(vmi, "disk0", "virtio", containerImage)
	vmi.Spec.Domain.Devices.Filesystems = append(vmi.Spec.Domain.Devices.Filesystems, v1.Filesystem{
		Name:     "disk1",
		Virtiofs: &v1.FilesystemVirtiofs{},
	})
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: "disk1",
		VolumeSource: v1.VolumeSource{
			DataVolume: &v1.DataVolumeSource{
				Name: dataVolumeName,
			},
		},
	})
	return vmi
}

func NewRandomVMIWithPVCFS(claimName string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMI()

	containerImage := cd.ContainerDiskFor(cd.ContainerDiskFedoraTestTooling)
	AddEphemeralDisk(vmi, "disk0", "virtio", containerImage)
	vmi = AddPVCFS(vmi, "disk1", claimName)
	return vmi
}

func NewRandomFedoraVMIWithDmidecode() *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDiskHighMemory(cd.ContainerDiskFor(cd.ContainerDiskFedoraTestTooling))
	return vmi
}

func NewRandomFedoraVMIWithVirtWhatCpuidHelper() *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDiskHighMemory(cd.ContainerDiskFor(cd.ContainerDiskFedoraTestTooling))
	return vmi
}

func GetFedoraToolsGuestAgentBlacklistUserData(commands string) string {
	return fmt.Sprintf(`#!/bin/bash
            echo -e "\n\nBLACKLIST_RPC=%s" | sudo tee -a /etc/sysconfig/qemu-ga
`, commands)
}

func NewRandomVMIWithEphemeralDiskAndUserdata(containerImage string, userData string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDisk(containerImage)
	AddUserData(vmi, "disk1", userData)
	return vmi
}

func NewRandomVMIWithEphemeralDiskAndConfigDriveUserdata(containerImage string, userData string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDisk(containerImage)
	AddCloudInitConfigDriveData(vmi, "disk1", userData, "", false)
	return vmi
}

func NewRandomVMIWithEphemeralDiskAndUserdataNetworkData(containerImage, userData, networkData string, b64encode bool) *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDisk(containerImage)
	AddCloudInitNoCloudData(vmi, "disk1", userData, networkData, b64encode)
	return vmi
}

func NewRandomVMIWithEphemeralDiskAndConfigDriveUserdataNetworkData(containerImage, userData, networkData string, b64encode bool) *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDisk(containerImage)
	AddCloudInitConfigDriveData(vmi, "disk1", userData, networkData, b64encode)
	return vmi
}

func AddUserData(vmi *v1.VirtualMachineInstance, name string, userData string) {
	AddCloudInitNoCloudData(vmi, name, userData, "", true)
}

func AddCloudInitNoCloudData(vmi *v1.VirtualMachineInstance, name, userData, networkData string, b64encode bool) {
	cloudInitNoCloudSource := v1.CloudInitNoCloudSource{}
	if b64encode {
		cloudInitNoCloudSource.UserDataBase64 = base64.StdEncoding.EncodeToString([]byte(userData))
		if networkData != "" {
			cloudInitNoCloudSource.NetworkDataBase64 = base64.StdEncoding.EncodeToString([]byte(networkData))
		}
	} else {
		cloudInitNoCloudSource.UserData = userData
		if networkData != "" {
			cloudInitNoCloudSource.NetworkData = networkData
		}
	}
	addCloudInitDiskAndVolume(vmi, name, v1.VolumeSource{CloudInitNoCloud: &cloudInitNoCloudSource})
}

func AddCloudInitConfigDriveData(vmi *v1.VirtualMachineInstance, name, userData, networkData string, b64encode bool) {
	cloudInitConfigDriveSource := v1.CloudInitConfigDriveSource{}
	if b64encode {
		cloudInitConfigDriveSource.UserDataBase64 = base64.StdEncoding.EncodeToString([]byte(userData))
		if networkData != "" {
			cloudInitConfigDriveSource.NetworkDataBase64 = base64.StdEncoding.EncodeToString([]byte(networkData))
		}
	} else {
		cloudInitConfigDriveSource.UserData = userData
		if networkData != "" {
			cloudInitConfigDriveSource.NetworkData = networkData
		}
	}
	addCloudInitDiskAndVolume(vmi, name, v1.VolumeSource{CloudInitConfigDrive: &cloudInitConfigDriveSource})
}

func addCloudInitDiskAndVolume(vmi *v1.VirtualMachineInstance, name string, volumeSource v1.VolumeSource) {
	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: name,
		DiskDevice: v1.DiskDevice{
			Disk: &v1.DiskTarget{
				Bus: "virtio",
			},
		},
	})
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name:         name,
		VolumeSource: volumeSource,
	})
}

func NewRandomVMIWithPVC(claimName string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMI()

	vmi = AddPVCDisk(vmi, "disk0", "virtio", claimName)
	return vmi
}

func NewRandomVMIWithPVCAndUserData(claimName, userData string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMI()

	vmi = AddPVCDisk(vmi, "disk0", "virtio", claimName)
	AddUserData(vmi, "disk1", userData)
	return vmi
}

func CreateBlockVolumePvAndPvc(size string) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	labelSelector := make(map[string]string)
	labelSelector["kubevirt-test"] = BlockDiskForTest
	labelSelector[cleanup.TestLabelForNamespace(util2.NamespaceTestDefault)] = ""

	_, err = virtCli.CoreV1().PersistentVolumes().Create(context.Background(), newBlockVolumePV(BlockDiskForTest, labelSelector, size), metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}

	_, err = virtCli.CoreV1().PersistentVolumeClaims((util2.NamespaceTestDefault)).Create(context.Background(), newBlockVolumePVC(BlockDiskForTest, labelSelector, size), metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}
}

func newBlockVolumePV(name string, labelSelector map[string]string, size string) *k8sv1.PersistentVolume {
	quantity, err := resource.ParseQuantity(size)
	util2.PanicOnError(err)

	storageClass := Config.StorageClassBlockVolume
	volumeMode := k8sv1.PersistentVolumeBlock

	// Note: the path depends on kubevirtci!
	// It's configured to have a device backed by a cirros image at exactly that place on node01
	// And the local storage provider also has access to it
	return &k8sv1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labelSelector,
		},
		Spec: k8sv1.PersistentVolumeSpec{
			AccessModes: []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce},
			Capacity: k8sv1.ResourceList{
				"storage": quantity,
			},
			StorageClassName: storageClass,
			VolumeMode:       &volumeMode,
			PersistentVolumeSource: k8sv1.PersistentVolumeSource{
				Local: &k8sv1.LocalVolumeSource{
					Path: "/mnt/local-storage/cirros-block-device",
				},
			},
			NodeAffinity: &k8sv1.VolumeNodeAffinity{
				Required: &k8sv1.NodeSelector{
					NodeSelectorTerms: []k8sv1.NodeSelectorTerm{
						{
							MatchExpressions: []k8sv1.NodeSelectorRequirement{
								{
									Key:      "kubernetes.io/hostname",
									Operator: k8sv1.NodeSelectorOpIn,
									Values:   []string{schedulableNode},
								},
							},
						},
					},
				},
			},
		},
	}
}

func newBlockVolumePVC(name string, labelSelector map[string]string, size string) *k8sv1.PersistentVolumeClaim {
	quantity, err := resource.ParseQuantity(size)
	util2.PanicOnError(err)

	storageClass := Config.StorageClassBlockVolume
	volumeMode := k8sv1.PersistentVolumeBlock

	return &k8sv1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: k8sv1.PersistentVolumeClaimSpec{
			AccessModes: []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce},
			Resources: k8sv1.ResourceRequirements{
				Requests: k8sv1.ResourceList{
					"storage": quantity,
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: labelSelector,
			},
			StorageClassName: &storageClass,
			VolumeMode:       &volumeMode,
		},
	}
}

func DeletePvAndPvc(name string) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	err = virtCli.CoreV1().PersistentVolumes().Delete(context.Background(), name, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		util2.PanicOnError(err)
	}

	err = virtCli.CoreV1().PersistentVolumeClaims((util2.NamespaceTestDefault)).Delete(context.Background(), name, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		util2.PanicOnError(err)
	}
}

func deleteBlockPVAndPVC() {
	DeletePvAndPvc(BlockDiskForTest)
}

func NewRandomVMIWithCDRom(claimName string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMI()

	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: "disk0",
		DiskDevice: v1.DiskDevice{
			CDRom: &v1.CDRomTarget{
				// Do not specify ReadOnly flag so that
				// default behavior can be tested
				Bus: "sata",
			},
		},
	})
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: "disk0",
		VolumeSource: v1.VolumeSource{
			PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{
				ClaimName: claimName,
			},
		},
	})
	return vmi
}

func NewRandomVMIWithEphemeralPVC(claimName string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMI()

	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: "disk0",
		DiskDevice: v1.DiskDevice{
			Disk: &v1.DiskTarget{
				Bus: "sata",
			},
		},
	})
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: "disk0",

		VolumeSource: v1.VolumeSource{
			Ephemeral: &v1.EphemeralVolumeSource{
				PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{
					ClaimName: claimName,
				},
			},
		},
	})
	return vmi
}

func AddHostDisk(vmi *v1.VirtualMachineInstance, path string, diskType v1.HostDiskType, name string) {
	hostDisk := v1.HostDisk{
		Path: path,
		Type: diskType,
	}
	if diskType == v1.HostDiskExistsOrCreate {
		hostDisk.Capacity = resource.MustParse(defaultDiskSize)
	}

	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: name,
		DiskDevice: v1.DiskDevice{
			Disk: &v1.DiskTarget{
				Bus: "virtio",
			},
		},
	})
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: name,
		VolumeSource: v1.VolumeSource{
			HostDisk: &hostDisk,
		},
	})
}

func NewRandomVMIWithHostDisk(diskPath string, diskType v1.HostDiskType, nodeName string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMI()
	AddHostDisk(vmi, diskPath, diskType, "host-disk")
	if nodeName != "" {
		vmi.Spec.Affinity = &k8sv1.Affinity{
			NodeAffinity: &k8sv1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &k8sv1.NodeSelector{
					NodeSelectorTerms: []k8sv1.NodeSelectorTerm{
						{
							MatchExpressions: []k8sv1.NodeSelectorRequirement{
								{
									Key:      "kubernetes.io/hostname",
									Operator: k8sv1.NodeSelectorOpIn,
									Values:   []string{nodeName},
								},
							},
						},
					},
				},
			},
		}
	}
	return vmi
}

func NewRandomVMIWithWatchdog() *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDisk(cd.ContainerDiskFor(cd.ContainerDiskAlpine))

	vmi.Spec.Domain.Devices.Watchdog = &v1.Watchdog{
		Name: "mywatchdog",
		WatchdogDevice: v1.WatchdogDevice{
			I6300ESB: &v1.I6300ESBWatchdog{
				Action: v1.WatchdogActionPoweroff,
			},
		},
	}
	return vmi
}

func NewRandomVMIWithConfigMap(configMapName string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithPVC(DiskAlpineHostPath)
	AddConfigMapDisk(vmi, configMapName, configMapName)
	return vmi
}

func AddConfigMapDisk(vmi *v1.VirtualMachineInstance, configMapName string, volumeName string) {
	AddConfigMapDiskWithCustomLabel(vmi, configMapName, volumeName, "")

}
func AddConfigMapDiskWithCustomLabel(vmi *v1.VirtualMachineInstance, configMapName string, volumeName string, volumeLabel string) {
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: volumeName,
		VolumeSource: v1.VolumeSource{
			ConfigMap: &v1.ConfigMapVolumeSource{
				LocalObjectReference: k8sv1.LocalObjectReference{
					Name: configMapName,
				},
				VolumeLabel: volumeLabel,
			},
		},
	})
	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: volumeName,
	})
}

func NewRandomVMIWithSecret(secretName string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithPVC(DiskAlpineHostPath)
	AddSecretDisk(vmi, secretName, secretName)
	return vmi
}

func AddSecretDisk(vmi *v1.VirtualMachineInstance, secretName string, volumeName string) {
	AddSecretDiskWithCustomLabel(vmi, secretName, volumeName, "")
}

func AddSecretDiskWithCustomLabel(vmi *v1.VirtualMachineInstance, secretName string, volumeName string, volumeLabel string) {
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: volumeName,
		VolumeSource: v1.VolumeSource{
			Secret: &v1.SecretVolumeSource{
				SecretName:  secretName,
				VolumeLabel: volumeLabel,
			},
		},
	})
	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: volumeName,
	})
}

func AddLabelDownwardAPIVolume(vmi *v1.VirtualMachineInstance, volumeName string) {
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: volumeName,
		VolumeSource: v1.VolumeSource{
			DownwardAPI: &v1.DownwardAPIVolumeSource{
				Fields: []k8sv1.DownwardAPIVolumeFile{
					{
						Path: "labels",
						FieldRef: &k8sv1.ObjectFieldSelector{
							FieldPath: "metadata.labels",
						},
					},
				},
				VolumeLabel: "",
			},
		},
	})

	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: volumeName,
	})
}

func AddDownwardMetricsVolume(vmi *v1.VirtualMachineInstance, volumeName string) {
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: volumeName,
		VolumeSource: v1.VolumeSource{
			DownwardMetrics: &v1.DownwardMetricsVolumeSource{},
		}})

	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: volumeName,
		DiskDevice: v1.DiskDevice{
			Disk: &v1.DiskTarget{
				Bus: "virtio",
			},
		},
	})
}

func NewRandomVMIWithServiceAccount(serviceAccountName string) *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithPVC(DiskAlpineHostPath)
	AddServiceAccountDisk(vmi, serviceAccountName)
	return vmi
}

func AddServiceAccountDisk(vmi *v1.VirtualMachineInstance, serviceAccountName string) {
	volumeName := serviceAccountName + "-disk"
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: volumeName,
		VolumeSource: v1.VolumeSource{
			ServiceAccount: &v1.ServiceAccountVolumeSource{
				ServiceAccountName: serviceAccountName,
			},
		},
	})
	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: serviceAccountName + "-disk",
	})
}

func NewRandomVMIWithSlirpInterfaceEphemeralDiskAndUserdata(containerImage string, userData string, Ports []v1.Port) *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDiskAndUserdata(containerImage, userData)
	vmi.Spec.Domain.Devices.Interfaces = []v1.Interface{{Name: "default", Ports: Ports, InterfaceBindingMethod: v1.InterfaceBindingMethod{Slirp: &v1.InterfaceSlirp{}}}}
	vmi.Spec.Networks = []v1.Network{*v1.DefaultPodNetwork()}

	return vmi
}

func NewRandomVMIWithMasqueradeInterfaceEphemeralDiskAndUserdata(containerImage string, userData string, Ports []v1.Port) *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDiskAndUserdata(containerImage, userData)
	vmi.Spec.Domain.Devices.Interfaces = []v1.Interface{{Name: "default", Ports: Ports, InterfaceBindingMethod: v1.InterfaceBindingMethod{Masquerade: &v1.InterfaceMasquerade{}}}}
	vmi.Spec.Networks = []v1.Network{*v1.DefaultPodNetwork()}

	return vmi
}

func AddExplicitPodNetworkInterface(vmi *v1.VirtualMachineInstance) {
	vmi.Spec.Domain.Devices.Interfaces = []v1.Interface{*v1.DefaultMasqueradeNetworkInterface()}
	vmi.Spec.Networks = []v1.Network{*v1.DefaultPodNetwork()}
}

func NewRandomVMIWithe1000NetworkInterface() *v1.VirtualMachineInstance {
	// Use alpine because cirros dhcp client starts prematurely before link is ready
	vmi := NewRandomVMIWithEphemeralDisk(cd.ContainerDiskFor(cd.ContainerDiskAlpine))
	AddExplicitPodNetworkInterface(vmi)
	vmi.Spec.Domain.Devices.Interfaces[0].Model = "e1000"
	return vmi
}

func NewRandomVMIWithCustomMacAddress() *v1.VirtualMachineInstance {
	vmi := NewRandomVMIWithEphemeralDisk(cd.ContainerDiskFor(cd.ContainerDiskAlpine))
	AddExplicitPodNetworkInterface(vmi)
	vmi.Spec.Domain.Devices.Interfaces[0].MacAddress = "de:ad:00:00:be:af"
	return vmi
}

// Block until DataVolume succeeds on storage with Immediate binding
// or is in WaitForFirstConsumer state on storage with WaitForFirstConsumer binding.
func WaitForDataVolumeReadyToStartVMI(obj runtime.Object, seconds int) {
	vmi, ok := obj.(*v1.VirtualMachineInstance)
	ExpectWithOffset(1, ok).To(BeTrue(), "Object is not of type *v1.VMI")
	WaitForDataVolumeReady(vmi.Namespace, vmi.Spec.Volumes[0].DataVolume.Name, seconds)
}

func WaitForDataVolumeReady(namespace, name string, seconds int) {
	waitForDataVolumePhase(namespace, name, seconds, cdiv1.WaitForFirstConsumer, cdiv1.Succeeded)
}

func WaitForDataVolumeImportInProgress(namespace, name string, seconds int) {
	waitForDataVolumePhase(namespace, name, seconds, cdiv1.WaitForFirstConsumer, cdiv1.ImportInProgress)
}

func WaitForDataVolumePhaseWFFC(namespace, name string, seconds int) {
	waitForDataVolumePhase(namespace, name, seconds, cdiv1.WaitForFirstConsumer)
}

func WaitForSuccessfulDataVolumeImport(dv *cdiv1.DataVolume, seconds int) {
	waitForDataVolumePhase(dv.Namespace, dv.Name, seconds, cdiv1.Succeeded)
}

func waitForDataVolumePhase(namespace, name string, seconds int, phase ...cdiv1.DataVolumePhase) {
	By("Checking that the DataVolume has succeeded")
	virtClient, err := kubecli.GetKubevirtClient()
	ExpectWithOffset(2, err).ToNot(HaveOccurred())

	EventuallyWithOffset(2,
		func() cdiv1.DataVolumePhase {
			dv, err := virtClient.CdiClient().CdiV1beta1().DataVolumes(namespace).Get(context.Background(), name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return cdiv1.PhaseUnset
			}
			ExpectWithOffset(2, err).ToNot(HaveOccurred())

			return dv.Status.Phase
		},
		time.Duration(seconds)*time.Second, 1*time.Second).
		Should(BeElementOf(phase),
			"Timed out waiting for DataVolume to enter Succeeded phase")
}

// Block until the specified VirtualMachineInstance reached either Failed or Running states
func WaitForVMIStartOrFailed(obj runtime.Object, seconds int, wp WarningsPolicy) (nodeName string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return waitForVMIPhase(ctx, []v1.VirtualMachineInstancePhase{v1.Running, v1.Failed}, obj, seconds, wp, true)
}

// Block until the specified VirtualMachineInstance started and return the target node name.
func waitForVMIStart(ctx context.Context, obj runtime.Object, seconds int, wp WarningsPolicy) (nodeName string) {
	return waitForVMIPhase(ctx, []v1.VirtualMachineInstancePhase{v1.Running}, obj, seconds, wp, false)
}

// Block until the specified VirtualMachineInstance scheduled and return the target node name.
func waitForVMIScheduling(obj runtime.Object, seconds int, wp WarningsPolicy) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitForVMIPhase(ctx, []v1.VirtualMachineInstancePhase{v1.Scheduling, v1.Scheduled, v1.Running}, obj, seconds, wp, false)
}

func waitForVMIPhase(ctx context.Context, phases []v1.VirtualMachineInstancePhase, obj runtime.Object, seconds int, wp WarningsPolicy, waitForFail bool) (nodeName string) {
	vmi, ok := obj.(*v1.VirtualMachineInstance)
	ExpectWithOffset(1, ok).To(BeTrue(), "Object is not of type *v1.VMI")

	virtClient, err := kubecli.GetKubevirtClient()
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	// Fetch the VirtualMachineInstance, to make sure we have a resourceVersion as a starting point for the watch
	// FIXME: This may start watching too late and we may miss some warnings
	if vmi.ResourceVersion == "" {
		vmi, err = virtClient.VirtualMachineInstance(vmi.Namespace).Get(vmi.Name, &metav1.GetOptions{})
		ExpectWithOffset(1, err).ToNot(HaveOccurred())
	}

	objectEventWatcher := NewObjectEventWatcher(vmi).SinceWatchedObjectResourceVersion().Timeout(time.Duration(seconds+2) * time.Second)
	if wp.FailOnWarnings == true {
		objectEventWatcher.SetWarningsPolicy(wp)
	}

	go func() {
		defer GinkgoRecover()
		objectEventWatcher.WaitFor(ctx, NormalEvent, v1.Started)
	}()

	timeoutMsg := fmt.Sprintf("Timed out waiting for VMI %s to enter %s phase(s)", vmi.Name, phases)
	// FIXME the event order is wrong. First the document should be updated
	EventuallyWithOffset(1, func() v1.VirtualMachineInstancePhase {
		vmi, err = virtClient.VirtualMachineInstance(vmi.Namespace).Get(vmi.Name, &metav1.GetOptions{})
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		nodeName = vmi.Status.NodeName
		Expect(vmi.Status.Phase == v1.Succeeded).To(BeFalse(), "VMI %s unexpectedly stopped. State: %s", vmi.Name, vmi.Status.Phase)
		// May need to wait for Failed state
		if !waitForFail {
			Expect(vmi.Status.Phase == v1.Failed).To(BeFalse(), "VMI %s unexpectedly stopped. State: %s", vmi.Name, vmi.Status.Phase)
		}
		return vmi.Status.Phase
	}, time.Duration(seconds)*time.Second, 1*time.Second).Should(BeElementOf(phases), timeoutMsg)

	return
}

func WaitForSuccessfulVMIStartIgnoreWarnings(vmi runtime.Object) string {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wp := WarningsPolicy{FailOnWarnings: false}
	return waitForVMIStart(ctx, vmi, 180, wp)
}

func WaitForSuccessfulVMIStartWithTimeout(vmi runtime.Object, seconds int) (nodeName string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wp := WarningsPolicy{FailOnWarnings: true}
	return waitForVMIStart(ctx, vmi, seconds, wp)
}

func WaitForSuccessfulVMIStartWithTimeoutIgnoreWarnings(vmi runtime.Object, seconds int) string {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wp := WarningsPolicy{FailOnWarnings: false}
	return waitForVMIStart(ctx, vmi, seconds, wp)
}

func WaitForPodToDisappearWithTimeout(podName string, seconds int) {
	virtClient, err := kubecli.GetKubevirtClient()
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	EventuallyWithOffset(1, func() bool {
		_, err := virtClient.CoreV1().Pods(util2.NamespaceTestDefault).Get(context.Background(), podName, metav1.GetOptions{})
		return errors.IsNotFound(err)
	}, seconds, 1*time.Second).Should(BeTrue())
}

func WaitForVirtualMachineToDisappearWithTimeout(vmi *v1.VirtualMachineInstance, seconds int) {
	virtClient, err := kubecli.GetKubevirtClient()
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	EventuallyWithOffset(1, func() error {
		_, err := virtClient.VirtualMachineInstance(vmi.Namespace).Get(vmi.Name, &metav1.GetOptions{})
		return err
	}, seconds, 1*time.Second).Should(SatisfyAll(HaveOccurred(), WithTransform(errors.IsNotFound, BeTrue())), "The VMI should be gone within the given timeout")
}

func WaitForMigrationToDisappearWithTimeout(migration *v1.VirtualMachineInstanceMigration, seconds int) {
	virtClient, err := kubecli.GetKubevirtClient()
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	EventuallyWithOffset(1, func() bool {
		_, err := virtClient.VirtualMachineInstanceMigration(migration.Namespace).Get(migration.Name, &metav1.GetOptions{})
		return errors.IsNotFound(err)
	}, seconds, 1*time.Second).Should(BeTrue())
}

func WaitForSuccessfulVMIStart(vmi runtime.Object) string {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return WaitForSuccessfulVMIStartWithContext(ctx, vmi)
}

func WaitForSuccessfulVMIStartWithContext(ctx context.Context, vmi runtime.Object) string {
	wp := WarningsPolicy{FailOnWarnings: true}
	return waitForVMIStart(ctx, vmi, 360, wp)
}

func WaitForSuccessfulVMIStartWithContextIgnoreSelectedWarnings(ctx context.Context, vmi runtime.Object, warningsIgnoreList []string) string {
	wp := WarningsPolicy{FailOnWarnings: true, WarningsIgnoreList: warningsIgnoreList}
	return waitForVMIStart(ctx, vmi, 360, wp)
}

func WaitUntilVMIReadyAsync(ctx context.Context, vmi *v1.VirtualMachineInstance, loginTo console.LoginToFactory) func() *v1.VirtualMachineInstance {
	var (
		wg       sync.WaitGroup
		readyVMI *v1.VirtualMachineInstance
	)
	wg.Add(1)
	go func() {
		defer GinkgoRecover()
		defer wg.Done()
		readyVMI = WaitUntilVMIReadyWithContext(ctx, vmi, loginTo)
	}()

	return func() *v1.VirtualMachineInstance {
		wg.Wait()
		return readyVMI
	}
}

func WaitUntilVMIReady(vmi *v1.VirtualMachineInstance, loginTo console.LoginToFactory) *v1.VirtualMachineInstance {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return WaitUntilVMIReadyWithContext(ctx, vmi, loginTo)
}

func WaitUntilVMIReadyIgnoreSelectedWarnings(vmi *v1.VirtualMachineInstance, loginTo console.LoginToFactory, warningsIgnoreList []string) *v1.VirtualMachineInstance {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return WaitUntilVMIReadyWithContextIgnoreSelectedWarnings(ctx, vmi, loginTo, warningsIgnoreList)
}

func WaitUntilVMIReadyWithContext(ctx context.Context, vmi *v1.VirtualMachineInstance, loginTo console.LoginToFactory) *v1.VirtualMachineInstance {
	// Wait for VirtualMachineInstance start
	WaitForSuccessfulVMIStartWithContext(ctx, vmi)
	return LoginToVM(vmi, loginTo)
}

func WaitUntilVMIReadyWithContextIgnoreSelectedWarnings(ctx context.Context, vmi *v1.VirtualMachineInstance, loginTo console.LoginToFactory, warningsIgnoreList []string) *v1.VirtualMachineInstance {
	// Wait for VirtualMachineInstance start
	WaitForSuccessfulVMIStartWithContextIgnoreSelectedWarnings(ctx, vmi, warningsIgnoreList)
	return LoginToVM(vmi, loginTo)
}

func LoginToVM(vmi *v1.VirtualMachineInstance, loginTo console.LoginToFactory) *v1.VirtualMachineInstance {
	// Fetch the new VirtualMachineInstance with updated status
	virtClient, err := kubecli.GetKubevirtClient()
	Expect(err).ToNot(HaveOccurred())
	vmi, err = virtClient.VirtualMachineInstance(vmi.Namespace).Get(vmi.Name, &metav1.GetOptions{})
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	// Lets make sure that the OS is up by waiting until we can login

	ExpectWithOffset(1, loginTo(vmi)).To(Succeed())

	return vmi
}

func NewInt32(x int32) *int32 {
	return &x
}

func NewRandomReplicaSetFromVMI(vmi *v1.VirtualMachineInstance, replicas int32) *v1.VirtualMachineInstanceReplicaSet {
	name := "replicaset" + rand.String(5)
	rs := &v1.VirtualMachineInstanceReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "replicaset" + rand.String(5)},
		Spec: v1.VirtualMachineInstanceReplicaSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"name": name},
			},
			Template: &v1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"name": name},
					Name:   vmi.ObjectMeta.Name,
				},
				Spec: vmi.Spec,
			},
		},
	}
	return rs
}

func NewBool(x bool) *bool {
	return &x
}

func RenderPrivilegedPod(name string, cmd []string, args []string) *k8sv1.Pod {
	pod := RenderPod(name, cmd, args)
	pod.Spec.HostPID = true
	pod.Spec.SecurityContext = &k8sv1.PodSecurityContext{
		RunAsUser: new(int64),
	}
	pod.Spec.Containers = []k8sv1.Container{
		renderPrivilegedContainerSpec(
			fmt.Sprintf("%s/vm-killer:%s", flags.KubeVirtUtilityRepoPrefix, flags.KubeVirtUtilityVersionTag),
			name,
			cmd,
			args),
	}

	return pod
}

func RenderPod(name string, cmd []string, args []string) *k8sv1.Pod {
	pod := k8sv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: name,
			Namespace:    util2.NamespaceTestDefault,
			Labels: map[string]string{
				v1.AppLabel: "test",
			},
		},
		Spec: k8sv1.PodSpec{
			RestartPolicy: k8sv1.RestartPolicyNever,
			Containers: []k8sv1.Container{
				renderContainerSpec(
					fmt.Sprintf("%s/vm-killer:%s", flags.KubeVirtUtilityRepoPrefix, flags.KubeVirtUtilityVersionTag),
					name,
					cmd,
					args),
			},
		},
	}

	return &pod
}

func renderContainerSpec(imgPath string, name string, cmd []string, args []string) k8sv1.Container {
	return k8sv1.Container{
		Name:    name,
		Image:   imgPath,
		Command: cmd,
		Args:    args,
	}
}

func renderPrivilegedContainerSpec(imgPath string, name string, cmd []string, args []string) k8sv1.Container {
	return k8sv1.Container{
		Name:    name,
		Image:   imgPath,
		Command: cmd,
		Args:    args,
		SecurityContext: &k8sv1.SecurityContext{
			Privileged: NewBool(true),
			RunAsUser:  new(int64),
		},
	}
}

func NewVirtctlCommand(args ...string) *cobra.Command {
	commandline := []string{}
	master := flag.Lookup("master").Value
	if master != nil && master.String() != "" {
		commandline = append(commandline, "--server", master.String())
	}
	kubeconfig := flag.Lookup("kubeconfig").Value
	if kubeconfig != nil && kubeconfig.String() != "" {
		commandline = append(commandline, "--kubeconfig", kubeconfig.String())
	}
	cmd := virtctl.NewVirtctlCommand()
	cmd.SetArgs(append(commandline, args...))
	return cmd
}

func NewRepeatableVirtctlCommand(args ...string) func() error {
	return func() error {
		cmd := NewVirtctlCommand(args...)
		return cmd.Execute()
	}
}

func ExecuteCommandOnCephToolbox(virtCli kubecli.KubevirtClient, command []string) (string, error) {
	pods, err := virtCli.CoreV1().Pods("rook-ceph").List(context.Background(), metav1.ListOptions{LabelSelector: "app=rook-ceph-tools"})
	if err != nil {
		return "", err
	}

	stdout, stderr, err := ExecuteCommandOnPodV2(virtCli, &pods.Items[0], "rook-ceph-tools", command)

	if err != nil {
		return "", fmt.Errorf("failed executing command on pod: %v: stderr %v: stdout: %v", err, stderr, stdout)
	}

	if len(stderr) > 0 {
		return "", fmt.Errorf("stderr: %v", stderr)
	}

	return stdout, nil
}

func ExecuteCommandOnPod(virtCli kubecli.KubevirtClient, pod *k8sv1.Pod, containerName string, command []string) (string, error) {
	stdout, stderr, err := ExecuteCommandOnPodV2(virtCli, pod, containerName, command)

	if err != nil {
		return "", fmt.Errorf("failed executing command on pod: %v: stderr %v: stdout: %v", err, stderr, stdout)
	}

	if len(stderr) > 0 {
		return "", fmt.Errorf("stderr: %v", stderr)
	}

	return stdout, nil
}

func ExecuteCommandOnPodV2(virtCli kubecli.KubevirtClient, pod *k8sv1.Pod, containerName string, command []string) (stdout, stderr string, err error) {
	var (
		stdoutBuf bytes.Buffer
		stderrBuf bytes.Buffer
	)

	req := virtCli.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod.Name).
		Namespace(pod.Namespace).
		SubResource("exec").
		Param("container", containerName)

	req.VersionedParams(&k8sv1.PodExecOptions{
		Container: containerName,
		Command:   command,
		Stdin:     false,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, scheme.ParameterCodec)

	config, err := kubecli.GetKubevirtClientConfig()
	if err != nil {
		return "", "", err
	}

	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return "", "", err
	}

	err = exec.Stream(remotecommand.StreamOptions{
		Stdout: &stdoutBuf,
		Stderr: &stderrBuf,
		Tty:    false,
	})

	return stdoutBuf.String(), stderrBuf.String(), err
}

func GetRunningVirtualMachineInstanceDomainXML(virtClient kubecli.KubevirtClient, vmi *v1.VirtualMachineInstance) (string, error) {
	vmiPod, err := getRunningPodByVirtualMachineInstance(vmi, util2.NamespaceTestDefault)
	if err != nil {
		return "", err
	}

	found := false
	containerIdx := 0
	for idx, container := range vmiPod.Spec.Containers {
		if container.Name == "compute" {
			containerIdx = idx
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("could not find compute container for pod")
	}

	// get current vmi
	freshVMI, err := virtClient.VirtualMachineInstance(vmi.Namespace).Get(vmi.Name, &metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("Failed to get vmi, %s", err)
	}

	command := []string{"virsh"}
	if kutil.IsNonRootVMI(freshVMI) {
		command = append(command, "-c")
		command = append(command, "qemu+unix:///session?socket=/var/run/libvirt/libvirt-sock")
	}
	command = append(command, []string{"dumpxml", vmi.Namespace + "_" + vmi.Name}...)

	stdout, stderr, err := ExecuteCommandOnPodV2(
		virtClient,
		vmiPod,
		vmiPod.Spec.Containers[containerIdx].Name,
		command,
	)
	if err != nil {
		return "", fmt.Errorf("could not dump libvirt domxml (remotely on pod): %v: %s", err, stderr)
	}
	return stdout, err
}

func LibvirtDomainIsPaused(virtClient kubecli.KubevirtClient, vmi *v1.VirtualMachineInstance) (bool, error) {
	vmiPod, err := getRunningPodByVirtualMachineInstance(vmi, util2.NamespaceTestDefault)
	if err != nil {
		return false, err
	}

	found := false
	containerIdx := 0
	for idx, container := range vmiPod.Spec.Containers {
		if container.Name == "compute" {
			containerIdx = idx
			found = true
		}
	}
	if !found {
		return false, fmt.Errorf("could not find compute container for pod")
	}

	stdout, stderr, err := ExecuteCommandOnPodV2(
		virtClient,
		vmiPod,
		vmiPod.Spec.Containers[containerIdx].Name,
		[]string{"virsh", "--quiet", "domstate", vmi.Namespace + "_" + vmi.Name},
	)
	if err != nil {
		return false, fmt.Errorf("could not get libvirt domstate (remotely on pod): %v: %s", err, stderr)
	}
	return strings.Contains(stdout, "paused"), nil
}

func LibvirtDomainIsPersistent(virtClient kubecli.KubevirtClient, vmi *v1.VirtualMachineInstance) (bool, error) {
	vmiPod, err := getRunningPodByVirtualMachineInstance(vmi, util2.NamespaceTestDefault)
	if err != nil {
		return false, err
	}

	found := false
	containerIdx := 0
	for idx, container := range vmiPod.Spec.Containers {
		if container.Name == "compute" {
			containerIdx = idx
			found = true
		}
	}
	if !found {
		return false, fmt.Errorf("could not find compute container for pod")
	}

	stdout, stderr, err := ExecuteCommandOnPodV2(
		virtClient,
		vmiPod,
		vmiPod.Spec.Containers[containerIdx].Name,
		[]string{"virsh", "--quiet", "list", "--persistent", "--name"},
	)
	if err != nil {
		return false, fmt.Errorf("could not dump libvirt domxml (remotely on pod): %v: %s", err, stderr)
	}
	return strings.Contains(stdout, vmi.Namespace+"_"+vmi.Name), nil
}

func BeforeAll(fn func()) {
	first := true
	BeforeEach(func() {
		if first {
			fn()
			first = false
		}
	})
}

func SkipIfNonRoot(virtClient kubecli.KubevirtClient, feature string) {
	if checks.HasFeature(virtconfig.NonRoot) {
		Skip(fmt.Sprintf("NonRoot implementation doesn't support %s", feature))
	}
}

func SkipIfMissingRequiredImage(virtClient kubecli.KubevirtClient, imageName string) {
	windowsPv, err := virtClient.CoreV1().PersistentVolumes().Get(context.Background(), imageName, metav1.GetOptions{})
	if err != nil || windowsPv.Status.Phase == k8sv1.VolumePending || windowsPv.Status.Phase == k8sv1.VolumeFailed {
		Skip(fmt.Sprintf("Skip tests that requires PV %s", imageName))
	} else if windowsPv.Status.Phase == k8sv1.VolumeReleased {
		windowsPv.Spec.ClaimRef = nil
		_, err = virtClient.CoreV1().PersistentVolumes().Update(context.Background(), windowsPv, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())
	}
}

func SkipIfNoRhelImage(virtClient kubecli.KubevirtClient) {
	rhelPv, err := virtClient.CoreV1().PersistentVolumes().Get(context.Background(), DiskRhel, metav1.GetOptions{})
	if err != nil || rhelPv.Status.Phase == k8sv1.VolumePending || rhelPv.Status.Phase == k8sv1.VolumeFailed {
		Skip(fmt.Sprintf("Skip RHEL tests that requires PVC %s", DiskRhel))
	} else if rhelPv.Status.Phase == k8sv1.VolumeReleased {
		rhelPv.Spec.ClaimRef = nil
		_, err = virtClient.CoreV1().PersistentVolumes().Update(context.Background(), rhelPv, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())
	}
}

func SkipIfUseFlannel(virtClient kubecli.KubevirtClient) {
	labelSelector := "app=flannel"
	flannelpod, err := virtClient.CoreV1().Pods(metav1.NamespaceSystem).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
	Expect(err).ToNot(HaveOccurred())
	if len(flannelpod.Items) > 0 {
		Skip("Skip networkpolicy test for flannel network")
	}
}

func GetHighestCPUNumberAmongNodes(virtClient kubecli.KubevirtClient) int {
	var cpus int64

	nodes, err := virtClient.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	for _, node := range nodes.Items {
		if v, ok := node.Status.Capacity[k8sv1.ResourceCPU]; ok && v.Value() > cpus {
			cpus = v.Value()
		}
	}

	return int(cpus)
}

func GetK8sCmdClient() string {
	// use oc if it exists, otherwise use kubectl
	if flags.KubeVirtOcPath != "" {
		return "oc"
	}

	return "kubectl"
}

func SkipIfNoCmd(cmdName string) {
	var cmdPath string
	switch strings.ToLower(cmdName) {
	case "oc":
		cmdPath = flags.KubeVirtOcPath
	case "kubectl":
		cmdPath = flags.KubeVirtKubectlPath
	case "virtctl":
		cmdPath = flags.KubeVirtVirtctlPath
	case "gocli":
		cmdPath = flags.KubeVirtGoCliPath
	}
	if cmdPath == "" {
		Skip(fmt.Sprintf("Skip test that requires %s binary", cmdName))
	}
}

func RunCommand(cmdName string, args ...string) (string, string, error) {
	return RunCommandWithNS(util2.NamespaceTestDefault, cmdName, args...)
}

func RunCommandWithNS(namespace string, cmdName string, args ...string) (string, string, error) {
	return RunCommandWithNSAndInput(namespace, nil, cmdName, args...)
}

func RunCommandWithNSAndInput(namespace string, input io.Reader, cmdName string, args ...string) (string, string, error) {
	commandString, cmd, err := CreateCommandWithNS(namespace, cmdName, args...)
	if err != nil {
		return "", "", err
	}

	var output, stderr bytes.Buffer
	captureOutputBuffers := func() (string, string) {
		trimNullChars := func(buf bytes.Buffer) string {
			return string(bytes.Trim(buf.Bytes(), "\x00"))
		}
		return trimNullChars(output), trimNullChars(stderr)
	}

	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, &output, &stderr

	if err := cmd.Run(); err != nil {
		outputString, stderrString := captureOutputBuffers()
		log.Log.Reason(err).With("command", commandString, "output", outputString, "stderr", stderrString).Error("command failed: cannot run command")
		return outputString, stderrString, fmt.Errorf("command failed: cannot run command %q: %v", commandString, err)
	}

	outputString, stderrString := captureOutputBuffers()
	return outputString, stderrString, nil
}

func CreateCommandWithNS(namespace string, cmdName string, args ...string) (string, *exec.Cmd, error) {
	cmdPath := ""
	commandString := func() string {
		c := cmdPath
		if cmdPath == "" {
			c = cmdName
		}
		return strings.Join(append([]string{c}, args...), " ")
	}

	cmdName = strings.ToLower(cmdName)
	switch cmdName {
	case "oc":
		cmdPath = flags.KubeVirtOcPath
	case "kubectl":
		cmdPath = flags.KubeVirtKubectlPath
	case "virtctl":
		cmdPath = flags.KubeVirtVirtctlPath
	case "gocli":
		cmdPath = flags.KubeVirtGoCliPath
	}

	if cmdPath == "" {
		err := fmt.Errorf("no %s binary specified", cmdName)
		log.Log.Reason(err).With("command", commandString()).Error("command failed")
		return "", nil, fmt.Errorf("command failed: %v", err)
	}

	kubeconfig := flag.Lookup("kubeconfig").Value
	if kubeconfig == nil || kubeconfig.String() == "" {
		err := goerrors.New("cannot find kubeconfig")
		log.Log.Reason(err).With("command", commandString()).Error("command failed")
		return "", nil, fmt.Errorf("command failed: %v", err)
	}

	master := flag.Lookup("master").Value
	if master != nil && master.String() != "" {
		args = append(args, "--server", master.String())
	}
	if namespace != "" {
		args = append([]string{"-n", namespace}, args...)
	}

	cmd := exec.Command(cmdPath, args...)
	kubeconfEnv := fmt.Sprintf("KUBECONFIG=%s", kubeconfig.String())
	cmd.Env = append(os.Environ(), kubeconfEnv)

	return commandString(), cmd, nil
}

func RunCommandPipe(commands ...[]string) (string, string, error) {
	return RunCommandPipeWithNS(util2.NamespaceTestDefault, commands...)
}

func RunCommandPipeWithNS(namespace string, commands ...[]string) (string, string, error) {
	commandPipeString := func() string {
		commandStrings := []string{}
		for _, command := range commands {
			commandStrings = append(commandStrings, strings.Join(command, " "))
		}
		return strings.Join(commandStrings, " | ")
	}

	if len(commands) < 2 {
		err := goerrors.New("requires at least two commands")
		log.Log.Reason(err).With("command", commandPipeString()).Error("command pipe failed")
		return "", "", fmt.Errorf("command pipe failed: %v", err)
	}

	for i, command := range commands {
		cmdPath := ""
		cmdName := strings.ToLower(command[0])
		switch cmdName {
		case "oc":
			cmdPath = flags.KubeVirtOcPath
		case "kubectl":
			cmdPath = flags.KubeVirtKubectlPath
		case "virtctl":
			cmdPath = flags.KubeVirtVirtctlPath
		}
		if cmdPath == "" {
			err := fmt.Errorf("no %s binary specified", cmdName)
			log.Log.Reason(err).With("command", commandPipeString()).Error("command pipe failed")
			return "", "", fmt.Errorf("command pipe failed: %v", err)
		}
		commands[i][0] = cmdPath
	}

	kubeconfig := flag.Lookup("kubeconfig").Value
	if kubeconfig == nil || kubeconfig.String() == "" {
		err := goerrors.New("cannot find kubeconfig")
		log.Log.Reason(err).With("command", commandPipeString()).Error("command pipe failed")
		return "", "", fmt.Errorf("command pipe failed: %v", err)
	}
	kubeconfEnv := fmt.Sprintf("KUBECONFIG=%s", kubeconfig.String())

	master := flag.Lookup("master").Value
	cmds := make([]*exec.Cmd, len(commands))
	for i := range cmds {
		if master != nil && master.String() != "" {
			commands[i] = append(commands[i], "--server", master.String())
		}
		if namespace != "" {
			commands[i] = append(commands[i], "-n", namespace)
		}
		cmds[i] = exec.Command(commands[i][0], commands[i][1:]...)
		cmds[i].Env = append(os.Environ(), kubeconfEnv)
	}

	var output, stderr bytes.Buffer
	captureOutputBuffers := func() (string, string) {
		trimNullChars := func(buf bytes.Buffer) string {
			return string(bytes.Trim(buf.Bytes(), "\x00"))
		}
		return trimNullChars(output), trimNullChars(stderr)
	}

	last := len(cmds) - 1
	for i, cmd := range cmds[:last] {
		var err error
		if cmds[i+1].Stdin, err = cmd.StdoutPipe(); err != nil {
			cmdArgString := strings.Join(cmd.Args, " ")
			log.Log.Reason(err).With("command", commandPipeString()).Errorf("command pipe failed: cannot attach stdout pipe to command %q", cmdArgString)
			return "", "", fmt.Errorf("command pipe failed: cannot attach stdout pipe to command %q: %v", cmdArgString, err)
		}
		cmd.Stderr = &stderr
	}
	cmds[last].Stdout, cmds[last].Stderr = &output, &stderr

	for _, cmd := range cmds {
		if err := cmd.Start(); err != nil {
			outputString, stderrString := captureOutputBuffers()
			cmdArgString := strings.Join(cmd.Args, " ")
			log.Log.Reason(err).With("command", commandPipeString(), "output", outputString, "stderr", stderrString).Errorf("command pipe failed: cannot start command %q", cmdArgString)
			return outputString, stderrString, fmt.Errorf("command pipe failed: cannot start command %q: %v", cmdArgString, err)
		}
	}

	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			outputString, stderrString := captureOutputBuffers()
			cmdArgString := strings.Join(cmd.Args, " ")
			log.Log.Reason(err).With("command", commandPipeString(), "output", outputString, "stderr", stderrString).Errorf("command pipe failed: error while waiting for command %q", cmdArgString)
			return outputString, stderrString, fmt.Errorf("command pipe failed: error while waiting for command %q: %v", cmdArgString, err)
		}
	}

	outputString, stderrString := captureOutputBuffers()
	return outputString, stderrString, nil
}

func GenerateVMJson(vm *v1.VirtualMachine, generateDirectory string) (string, error) {
	data, err := json.Marshal(vm)
	if err != nil {
		return "", fmt.Errorf("failed to generate json for vm %s", vm.Name)
	}

	jsonFile := filepath.Join(generateDirectory, fmt.Sprintf("%s.json", vm.Name))
	err = ioutil.WriteFile(jsonFile, data, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to write json file %s", jsonFile)
	}
	return jsonFile, nil
}

func GenerateVMIJson(vmi *v1.VirtualMachineInstance, generateDirectory string) (string, error) {
	data, err := json.Marshal(vmi)
	if err != nil {
		return "", fmt.Errorf("failed to generate json for vmi %s", vmi.Name)
	}

	jsonFile := filepath.Join(generateDirectory, fmt.Sprintf("%s.json", vmi.Name))
	err = ioutil.WriteFile(jsonFile, data, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to write json file %s", jsonFile)
	}
	return jsonFile, nil
}

func GenerateTemplateJson(template *vmsgen.Template, generateDirectory string) (string, error) {
	data, err := json.Marshal(template)
	if err != nil {
		return "", fmt.Errorf("failed to generate json for template %q: %v", template.Name, err)
	}

	jsonFile := filepath.Join(generateDirectory, template.Name+".json")
	if err = ioutil.WriteFile(jsonFile, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write json to file %q: %v", jsonFile, err)
	}
	return jsonFile, nil
}

func NotDeleted(vmis *v1.VirtualMachineInstanceList) (notDeleted []v1.VirtualMachineInstance) {
	for _, vmi := range vmis.Items {
		if vmi.DeletionTimestamp == nil {
			notDeleted = append(notDeleted, vmi)
		}
	}
	return
}

func Running(vmis *v1.VirtualMachineInstanceList) (running []v1.VirtualMachineInstance) {
	for _, vmi := range vmis.Items {
		if vmi.DeletionTimestamp == nil && vmi.Status.Phase == v1.Running {
			running = append(running, vmi)
		}
	}
	return
}

func UnfinishedVMIPodSelector(vmi *v1.VirtualMachineInstance) metav1.ListOptions {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	vmi, err = virtClient.VirtualMachineInstance(vmi.Namespace).Get(vmi.Name, &metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred())

	fieldSelectorStr := "status.phase!=" + string(k8sv1.PodFailed) +
		",status.phase!=" + string(k8sv1.PodSucceeded)

	if vmi.Status.NodeName != "" {
		fieldSelectorStr = fieldSelectorStr +
			",spec.nodeName=" + vmi.Status.NodeName
	}

	fieldSelector := fields.ParseSelectorOrDie(fieldSelectorStr)
	labelSelector, err := labels.Parse(fmt.Sprintf(v1.AppLabel + "=virt-launcher," + v1.CreatedByLabel + "=" + string(vmi.GetUID())))
	if err != nil {
		panic(err)
	}
	return metav1.ListOptions{FieldSelector: fieldSelector.String(), LabelSelector: labelSelector.String()}
}

func RemoveHostDiskImage(diskPath string, nodeName string) {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)
	path := filepath.Join("/proc/1/root", diskPath)
	virtHandlerPod, err := kubecli.NewVirtHandlerClient(virtClient).Namespace(flags.KubeVirtInstallNamespace).ForNode(nodeName).Pod()
	Expect(err).ToNot(HaveOccurred())
	_, _, err = ExecuteCommandOnPodV2(virtClient, virtHandlerPod, "virt-handler", []string{"rm", "-rf", path})
	Expect(err).ToNot(HaveOccurred())
}

func CreateISCSITargetPOD(containerDiskName cd.ContainerDisk) *k8sv1.Pod {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)
	image := fmt.Sprintf("%s/cdi-http-import-server:%s", flags.KubeVirtUtilityRepoPrefix, flags.KubeVirtUtilityVersionTag)
	resources := k8sv1.ResourceRequirements{}
	resources.Limits = make(k8sv1.ResourceList)
	resources.Limits[k8sv1.ResourceMemory] = resource.MustParse("512M")
	pod := &k8sv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: ISCSITargetName,
			Labels: map[string]string{
				v1.AppLabel: ISCSITargetName,
			},
		},
		Spec: k8sv1.PodSpec{
			RestartPolicy: k8sv1.RestartPolicyNever,
			Containers: []k8sv1.Container{
				{
					Name:      ISCSITargetName,
					Image:     image,
					Resources: resources,
				},
			},
		},
	}
	if containerDiskName == cd.ContainerDiskEmpty {
		asEmpty := []k8sv1.EnvVar{
			{
				Name:  "AS_EMPTY",
				Value: "true",
			},
		}
		pod.Spec.Containers[0].Env = asEmpty
	} else {
		imageEnv := []k8sv1.EnvVar{
			{
				Name:  "AS_ISCSI",
				Value: "true",
			},
			{
				Name:  "IMAGE_NAME",
				Value: fmt.Sprintf("%s", containerDiskName),
			},
		}
		pod.Spec.Containers[0].Env = imageEnv
	}

	pod, err = virtClient.CoreV1().Pods(util2.NamespaceTestDefault).Create(context.Background(), pod, metav1.CreateOptions{})
	util2.PanicOnError(err)

	getStatus := func() k8sv1.PodPhase {
		pod, err := virtClient.CoreV1().Pods(util2.NamespaceTestDefault).Get(context.Background(), pod.Name, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		return pod.Status.Phase
	}
	Eventually(getStatus, 120, 1).Should(Equal(k8sv1.PodRunning))

	pod, err = virtClient.CoreV1().Pods(util2.NamespaceTestDefault).Get(context.Background(), pod.Name, metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred(), "should get ISCSI target pod after phase changed to Running")

	return pod
}

func CreateISCSIPvAndPvc(name string, size string, iscsiTargetIP string, accessMode k8sv1.PersistentVolumeAccessMode, volumeMode k8sv1.PersistentVolumeMode) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	_, err = virtCli.CoreV1().PersistentVolumes().Create(context.Background(), NewISCSIPV(name, size, iscsiTargetIP, accessMode, volumeMode), metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}

	_, err = virtCli.CoreV1().PersistentVolumeClaims((util2.NamespaceTestDefault)).Create(context.Background(), newISCSIPVC(name, size, accessMode, volumeMode), metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}
}

func NewISCSIPV(name, size, iscsiTargetIP string, accessMode k8sv1.PersistentVolumeAccessMode, volumeMode k8sv1.PersistentVolumeMode) *k8sv1.PersistentVolume {
	quantity, err := resource.ParseQuantity(size)
	util2.PanicOnError(err)

	storageClass := Config.StorageClassLocal

	return &k8sv1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				cleanup.TestLabelForNamespace(util2.NamespaceTestDefault): "",
			},
		},
		Spec: k8sv1.PersistentVolumeSpec{
			AccessModes: []k8sv1.PersistentVolumeAccessMode{accessMode},
			Capacity: k8sv1.ResourceList{
				"storage": quantity,
			},
			StorageClassName: storageClass,
			VolumeMode:       &volumeMode,
			PersistentVolumeSource: k8sv1.PersistentVolumeSource{
				ISCSI: &k8sv1.ISCSIPersistentVolumeSource{
					TargetPortal: iscsiTargetIP,
					IQN:          "iqn.2018-01.io.kubevirt:wrapper",
					Lun:          1,
					ReadOnly:     false,
				},
			},
		},
	}
}

func newISCSIPVC(name string, size string, accessMode k8sv1.PersistentVolumeAccessMode, volumeMode k8sv1.PersistentVolumeMode) *k8sv1.PersistentVolumeClaim {
	quantity, err := resource.ParseQuantity(size)
	util2.PanicOnError(err)

	storageClass := Config.StorageClassLocal

	return &k8sv1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: k8sv1.PersistentVolumeClaimSpec{
			AccessModes: []k8sv1.PersistentVolumeAccessMode{accessMode},
			Resources: k8sv1.ResourceRequirements{
				Requests: k8sv1.ResourceList{
					"storage": quantity,
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					cleanup.TestLabelForNamespace(util2.NamespaceTestDefault): "",
				},
			},
			StorageClassName: &storageClass,
			VolumeMode:       &volumeMode,
		},
	}
}

func CreateNFSPvAndPvc(name string, namespace string, size string, nfsTargetIP string, os string) {
	virtCli, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	_, err = virtCli.CoreV1().PersistentVolumes().Create(context.Background(), newNFSPV(name, namespace, size, nfsTargetIP, os), metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}

	_, err = virtCli.CoreV1().PersistentVolumeClaims((namespace)).Create(context.Background(), newNFSPVC(name, namespace, size, os), metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		util2.PanicOnError(err)
	}
}

func newNFSPV(name string, namespace string, size string, nfsTargetIP string, os string) *k8sv1.PersistentVolume {
	quantity := resource.MustParse(size)

	storageClass := Config.StorageClassLocal
	volumeMode := k8sv1.PersistentVolumeFilesystem

	nfsTargetIP = ip.NormalizeIPAddress(nfsTargetIP)

	return &k8sv1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"kubevirt.io/test":                       os,
				cleanup.TestLabelForNamespace(namespace): "",
			},
		},
		Spec: k8sv1.PersistentVolumeSpec{
			AccessModes: []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteMany},
			Capacity: k8sv1.ResourceList{
				"storage": quantity,
			},
			StorageClassName: storageClass,
			VolumeMode:       &volumeMode,
			PersistentVolumeSource: k8sv1.PersistentVolumeSource{
				NFS: &k8sv1.NFSVolumeSource{
					Server: nfsTargetIP,
					Path:   "/",
				},
			},
		},
	}
}

func newNFSPVC(name string, namespace string, size string, os string) *k8sv1.PersistentVolumeClaim {
	quantity, err := resource.ParseQuantity(size)
	util2.PanicOnError(err)

	storageClass := Config.StorageClassLocal
	volumeMode := k8sv1.PersistentVolumeFilesystem

	return &k8sv1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: k8sv1.PersistentVolumeClaimSpec{
			AccessModes: []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteMany},
			Resources: k8sv1.ResourceRequirements{
				Requests: k8sv1.ResourceList{
					"storage": quantity,
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"kubevirt.io/test":                       os,
					cleanup.TestLabelForNamespace(namespace): "",
				},
			},
			StorageClassName: &storageClass,
			VolumeMode:       &volumeMode,
		},
	}
}

func CreateHostDiskImage(diskPath string) *k8sv1.Pod {
	hostPathType := k8sv1.HostPathDirectoryOrCreate
	dir := filepath.Dir(diskPath)

	command := fmt.Sprintf(`dd if=/dev/zero of=%s bs=1 count=0 seek=1G && ls -l %s`, diskPath, dir)
	if checks.HasFeature(virtconfig.NonRoot) {
		command = command + fmt.Sprintf(" && chown 107:107 %s", diskPath)
	}

	args := []string{command}
	pod := RenderHostPathPod("hostdisk-create-job", dir, hostPathType, k8sv1.MountPropagationNone, []string{"/bin/bash", "-c"}, args)

	return pod
}

func RenderHostPathPod(podName string, dir string, hostPathType k8sv1.HostPathType, mountPropagation k8sv1.MountPropagationMode, cmd []string, args []string) *k8sv1.Pod {
	pod := RenderPrivilegedPod(podName, cmd, args)
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, k8sv1.VolumeMount{
		Name:             "hostpath-mount",
		MountPropagation: &mountPropagation,
		MountPath:        dir,
	})
	pod.Spec.Volumes = append(pod.Spec.Volumes, k8sv1.Volume{
		Name: "hostpath-mount",
		VolumeSource: k8sv1.VolumeSource{
			HostPath: &k8sv1.HostPathVolumeSource{
				Path: dir,
				Type: &hostPathType,
			},
		},
	})

	return pod
}

func GetNodeWithHugepages(virtClient kubecli.KubevirtClient, hugepages k8sv1.ResourceName) *k8sv1.Node {
	nodes, err := virtClient.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	for _, node := range nodes.Items {
		if v, ok := node.Status.Capacity[hugepages]; ok && !v.IsZero() {
			return &node
		}
	}
	return nil
}

// SkipIfVersionBelow will skip tests if it runs on an environment with k8s version below specified
func SkipIfVersionBelow(message string, expectedVersion string) {
	curVersion, err := cluster.GetKubernetesVersion()
	Expect(err).NotTo(HaveOccurred())

	if curVersion < expectedVersion {
		Skip(message)
	}
}

func SkipIfVersionAboveOrEqual(message string, expectedVersion string) {
	curVersion, err := cluster.GetKubernetesVersion()
	Expect(err).NotTo(HaveOccurred())

	if curVersion >= expectedVersion {
		Skip(message)
	}
}

func SkipIfOpenShift(message string) {
	if IsOpenShift() {
		Skip("Openshift detected: " + message)
	}
}

func SkipIfOpenShiftAndBelowOrEqualVersion(message string, version string) {
	curVersion, err := cluster.GetKubernetesVersion()
	Expect(err).NotTo(HaveOccurred())

	// version is above
	if curVersion > version {
		return
	}

	if IsOpenShift() {
		Skip(message)
	}
}

func SkipIfOpenShift4(message string) {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	if t, err := cluster.IsOnOpenShift(virtClient); err != nil {
		util2.PanicOnError(err)
	} else if t && cluster.GetOpenShiftMajorVersion(virtClient) == cluster.OpenShift4Major {
		Skip(message)
	}
}

func SkipIfMigrationIsNotPossible() {
	if !HasLiveMigration() {
		Skip("LiveMigration feature gate is not enabled in kubevirt-config")
	}

	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	nodes := util2.GetAllSchedulableNodes(virtClient)
	Expect(nodes.Items).ToNot(BeEmpty(), "There should be some compute node")

	if len(nodes.Items) < 2 {
		Skip("Migration tests require at least 2 nodes")
	}
}

// StartVmOnNode starts a VMI on the specified node
func StartVmOnNode(vmi *v1.VirtualMachineInstance, nodeName string) *v1.VirtualMachineInstance {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	vmi.Spec.Affinity = &k8sv1.Affinity{
		NodeAffinity: &k8sv1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &k8sv1.NodeSelector{
				NodeSelectorTerms: []k8sv1.NodeSelectorTerm{
					{
						MatchExpressions: []k8sv1.NodeSelectorRequirement{
							{Key: "kubernetes.io/hostname", Operator: k8sv1.NodeSelectorOpIn, Values: []string{nodeName}},
						},
					},
				},
			},
		},
	}

	vmi, err = virtClient.VirtualMachineInstance(util2.NamespaceTestDefault).Create(vmi)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	WaitForSuccessfulVMIStart(vmi)
	return vmi
}

// RunCommandOnVmiPod runs specified command on the virt-launcher pod
func RunCommandOnVmiPod(vmi *v1.VirtualMachineInstance, command []string) string {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	pods, err := virtClient.CoreV1().Pods(util2.NamespaceTestDefault).List(context.Background(), UnfinishedVMIPodSelector(vmi))
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	ExpectWithOffset(1, pods.Items).NotTo(BeEmpty())
	vmiPod := pods.Items[0]

	output, err := ExecuteCommandOnPod(
		virtClient,
		&vmiPod,
		"compute",
		command,
	)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	return output
}

// GetNodeLibvirtCapabilities returns node libvirt capabilities
func GetNodeLibvirtCapabilities(vmi *v1.VirtualMachineInstance) string {
	return RunCommandOnVmiPod(vmi, []string{"virsh", "-r", "capabilities"})
}

// GetNodeCPUInfo returns output of lscpu on the pod that runs on the specified node
func GetNodeCPUInfo(vmi *v1.VirtualMachineInstance) string {
	return RunCommandOnVmiPod(vmi, []string{"lscpu"})
}

func NewRandomVirtualMachine(vmi *v1.VirtualMachineInstance, running bool) *v1.VirtualMachine {
	name := vmi.Name
	namespace := vmi.Namespace
	labels := map[string]string{"name": name}
	for k, v := range vmi.Labels {
		labels[k] = v
	}
	vm := &v1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1.VirtualMachineSpec{
			Running: &running,
			Template: &v1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:    labels,
					Name:      name + "makeitinteresting", // this name should have no effect
					Namespace: namespace,
				},
				Spec: vmi.Spec,
			},
		},
	}
	vm.SetGroupVersionKind(schema.GroupVersionKind{Group: v1.GroupVersion.Group, Kind: "VirtualMachine", Version: v1.GroupVersion.Version})
	return vm
}

func StopVirtualMachineWithTimeout(vm *v1.VirtualMachine, timeout time.Duration) *v1.VirtualMachine {
	By("Stopping the VirtualMachineInstance")
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)
	running := false
	Eventually(func() error {
		updatedVM, err := virtClient.VirtualMachine(vm.Namespace).Get(vm.Name, &metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		updatedVM.Spec.Running = &running
		_, err = virtClient.VirtualMachine(updatedVM.Namespace).Update(updatedVM)
		return err
	}, timeout, 1*time.Second).ShouldNot(HaveOccurred())
	updatedVM, err := virtClient.VirtualMachine(vm.Namespace).Get(vm.Name, &metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred())
	// Observe the VirtualMachineInstance deleted
	Eventually(func() bool {
		_, err = virtClient.VirtualMachineInstance(updatedVM.Namespace).Get(updatedVM.Name, &metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true
		}
		return false
	}, timeout, 1*time.Second).Should(BeTrue(), "The vmi did not disappear")
	By("VM has not the running condition")
	Eventually(func() bool {
		vm, err := virtClient.VirtualMachine(updatedVM.Namespace).Get(updatedVM.Name, &metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		return vm.Status.Ready
	}, timeout, 1*time.Second).Should(BeFalse())
	return updatedVM
}

func StopVirtualMachine(vm *v1.VirtualMachine) *v1.VirtualMachine {
	return StopVirtualMachineWithTimeout(vm, time.Second*300)
}

func StartVirtualMachine(vm *v1.VirtualMachine) *v1.VirtualMachine {
	By("Starting the VirtualMachineInstance")
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)
	running := true
	Eventually(func() error {
		updatedVM, err := virtClient.VirtualMachine(vm.Namespace).Get(vm.Name, &metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		updatedVM.Spec.Running = &running
		_, err = virtClient.VirtualMachine(updatedVM.Namespace).Update(updatedVM)
		return err
	}, 300*time.Second, 1*time.Second).ShouldNot(HaveOccurred())
	updatedVM, err := virtClient.VirtualMachine(vm.Namespace).Get(vm.Name, &metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred())
	// Observe the VirtualMachineInstance created
	Eventually(func() error {
		_, err := virtClient.VirtualMachineInstance(updatedVM.Namespace).Get(updatedVM.Name, &metav1.GetOptions{})
		return err
	}, 300*time.Second, 1*time.Second).Should(Succeed())
	By("VMI has the running condition")
	Eventually(func() bool {
		vm, err := virtClient.VirtualMachine(updatedVM.Namespace).Get(updatedVM.Name, &metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		return vm.Status.Ready
	}, 300*time.Second, 1*time.Second).Should(BeTrue())
	return updatedVM
}

func DisableFeatureGate(feature string) {
	if !checks.HasFeature(feature) {
		return
	}
	virtClient, err := kubecli.GetKubevirtClient()
	Expect(err).ToNot(HaveOccurred())

	kv := util2.GetCurrentKv(virtClient)
	if kv.Spec.Configuration.DeveloperConfiguration == nil {
		kv.Spec.Configuration.DeveloperConfiguration = &v1.DeveloperConfiguration{
			FeatureGates: []string{},
		}
	}

	newArray := []string{}
	featureGates := kv.Spec.Configuration.DeveloperConfiguration.FeatureGates
	for _, fg := range featureGates {
		if fg == feature {
			continue
		}

		newArray = append(newArray, fg)
	}

	kv.Spec.Configuration.DeveloperConfiguration.FeatureGates = newArray

	UpdateKubeVirtConfigValueAndWait(kv.Spec.Configuration)
}

func EnableFeatureGate(feature string) *v1.KubeVirt {
	virtClient, err := kubecli.GetKubevirtClient()
	Expect(err).ToNot(HaveOccurred())

	kv := util2.GetCurrentKv(virtClient)
	if checks.HasFeature(feature) {
		return kv
	}

	if kv.Spec.Configuration.DeveloperConfiguration == nil {
		kv.Spec.Configuration.DeveloperConfiguration = &v1.DeveloperConfiguration{
			FeatureGates: []string{},
		}
	}

	kv.Spec.Configuration.DeveloperConfiguration.FeatureGates = append(kv.Spec.Configuration.DeveloperConfiguration.FeatureGates, feature)

	return UpdateKubeVirtConfigValueAndWait(kv.Spec.Configuration)
}

func HasDataVolumeCRD() bool {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	ext, err := extclient.NewForConfig(virtClient.Config())
	util2.PanicOnError(err)

	_, err = ext.ApiextensionsV1().CustomResourceDefinitions().Get(context.Background(), "datavolumes.cdi.kubevirt.io", metav1.GetOptions{})

	if err != nil {
		return false
	}
	return true
}

func HasCDI() bool {
	return HasDataVolumeCRD()
}

func HasBindingModeWaitForFirstConsumer() bool {
	virtClient, err := kubecli.GetKubevirtClient()
	Expect(err).ToNot(HaveOccurred())
	storageClass, err := virtClient.StorageV1().StorageClasses().Get(context.Background(), Config.StorageClassLocal, metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred())
	if err != nil {
		return false
	}
	return storageClass.VolumeBindingMode != nil &&
		*storageClass.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer
}

func GetCephStorageClass() (string, bool) {
	virtClient, err := kubecli.GetKubevirtClient()
	Expect(err).ToNot(HaveOccurred())
	storageClassList, err := virtClient.StorageV1().StorageClasses().List(context.Background(), metav1.ListOptions{})
	Expect(err).ToNot(HaveOccurred())
	for _, storageClass := range storageClassList.Items {
		switch storageClass.Provisioner {
		case "rook-ceph.rbd.csi.ceph.com", "csi-rbdplugin", "openshift-storage.rbd.csi.ceph.com":
			return storageClass.Name, true
		}
	}
	return "", false
}

func HasExperimentalIgnitionSupport() bool {
	return checks.HasFeature("ExperimentalIgnitionSupport")
}

func HasLiveMigration() bool {
	return checks.HasFeature("LiveMigration")
}

func GetVmPodName(virtCli kubecli.KubevirtClient, vmi *v1.VirtualMachineInstance) string {
	namespace := vmi.GetObjectMeta().GetNamespace()
	uid := vmi.GetObjectMeta().GetUID()
	labelSelector := fmt.Sprintf(v1.CreatedByLabel + "=" + string(uid))

	pods, err := virtCli.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
	Expect(err).ToNot(HaveOccurred())

	podName := ""
	for _, pod := range pods.Items {
		if pod.ObjectMeta.DeletionTimestamp == nil {
			podName = pod.ObjectMeta.Name
			break
		}
	}
	Expect(podName).ToNot(BeEmpty())

	return podName
}

func AppendEmptyDisk(vmi *v1.VirtualMachineInstance, diskName, busName, diskSize string) {
	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, v1.Disk{
		Name: diskName,
		DiskDevice: v1.DiskDevice{
			Disk: &v1.DiskTarget{
				Bus: busName,
			},
		},
	})
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, v1.Volume{
		Name: diskName,
		VolumeSource: v1.VolumeSource{
			EmptyDisk: &v1.EmptyDiskSource{
				Capacity: resource.MustParse(diskSize),
			},
		},
	})
}

func GetRunningVMIDomainSpec(vmi *v1.VirtualMachineInstance) (*launcherApi.DomainSpec, error) {
	runningVMISpec := launcherApi.DomainSpec{}
	cli, err := kubecli.GetKubevirtClient()
	if err != nil {
		return nil, err
	}

	domXML, err := GetRunningVirtualMachineInstanceDomainXML(cli, vmi)
	if err != nil {
		return nil, err
	}

	err = xml.Unmarshal([]byte(domXML), &runningVMISpec)
	return &runningVMISpec, err
}

func ForwardPorts(pod *k8sv1.Pod, ports []string, stop chan struct{}, readyTimeout time.Duration) error {
	errChan := make(chan error, 1)
	readyChan := make(chan struct{})
	go func() {
		cli, err := kubecli.GetKubevirtClient()
		if err != nil {
			errChan <- err
			return
		}

		req := cli.CoreV1().RESTClient().Post().
			Resource("pods").
			Namespace(pod.Namespace).
			Name(pod.Name).
			SubResource("portforward")

		config, err := kubecli.GetKubevirtClientConfig()
		if err != nil {
			errChan <- err
			return
		}
		transport, upgrader, err := spdy.RoundTripperFor(config)
		if err != nil {
			errChan <- err
			return
		}
		dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())
		forwarder, err := portforward.New(dialer, ports, stop, readyChan, GinkgoWriter, GinkgoWriter)
		if err != nil {
			errChan <- err
			return
		}
		err = forwarder.ForwardPorts()
		if err != nil {
			errChan <- err
		}
	}()

	select {
	case err := <-errChan:
		return err
	case <-readyChan:
		return nil
	case <-time.After(readyTimeout):
		return fmt.Errorf("failed to forward ports, timed out")
	}
}

func GenerateHelloWorldServer(vmi *v1.VirtualMachineInstance, testPort int, protocol string) {
	Expect(libnet.WithIPv6(console.LoginToCirros)(vmi)).To(Succeed())

	serverCommand := fmt.Sprintf("screen -d -m sudo nc -klp %d -e echo -e 'Hello World!'\n", testPort)
	if protocol == "udp" {
		// nc has to be in a while loop in case of UDP, since it exists after one message
		serverCommand = fmt.Sprintf("screen -d -m sh -c \"while true; do nc -uklp %d -e echo -e 'Hello UDP World!';done\"\n", testPort)
	}
	Expect(console.SafeExpectBatch(vmi, []expect.Batcher{
		&expect.BSnd{S: serverCommand},
		&expect.BExp{R: console.PromptExpression},
		&expect.BSnd{S: "echo $?\n"},
		&expect.BExp{R: console.RetValue("0")},
	}, 60)).To(Succeed())
}

// UpdateClusterConfigValueAndWait updates the given configuration in the kubevirt config map and then waits
// to allow the configuration events to be propagated to the consumers.
func UpdateClusterConfigValueAndWait(key string, value string) string {
	if config.GinkgoConfig.ParallelTotal > 1 {
		Fail("Tests which alter the global kubevirt configuration must not be executed in parallel")
	}
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)
	cfgMap, err := virtClient.CoreV1().ConfigMaps(flags.KubeVirtInstallNamespace).Get(context.Background(), virtconfig.ConfigMapName, metav1.GetOptions{})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	oldValue := cfgMap.Data[key]
	if cfgMap.Data[key] == value {
		return value
	}
	cfgMap.Data[key] = value
	cfg, err := virtClient.CoreV1().ConfigMaps(flags.KubeVirtInstallNamespace).Update(context.Background(), cfgMap, metav1.UpdateOptions{})
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	waitForConfigToBePropagated(cfg.ResourceVersion)
	log.DefaultLogger().Infof("Deployment is in sync with config resource version %s", cfg.ResourceVersion)
	return oldValue
}

// UpdateKubeVirtConfigValueAndWait updates the given configuration in the kubevirt custom resource
// and then waits  to allow the configuration events to be propagated to the consumers.
func UpdateKubeVirtConfigValueAndWait(kvConfig v1.KubeVirtConfiguration) *v1.KubeVirt {

	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	kv := util2.GetCurrentKv(virtClient)
	old, err := json.Marshal(kv)
	Expect(err).ToNot(HaveOccurred())

	if reflect.DeepEqual(kv.Spec.Configuration, kvConfig) {
		return kv
	}

	if config.GinkgoConfig.ParallelTotal > 1 {
		Fail("Tests which alter the global kubevirt configuration must not be executed in parallel")
	}

	updatedKV := kv.DeepCopy()
	updatedKV.Spec.Configuration = kvConfig
	newJson, err := json.Marshal(updatedKV)
	Expect(err).ToNot(HaveOccurred())

	patch, err := strategicpatch.CreateTwoWayMergePatch(old, newJson, kv)
	Expect(err).ToNot(HaveOccurred())

	kv, err = virtClient.KubeVirt(kv.Namespace).Patch(kv.GetName(), types.MergePatchType, patch)
	Expect(err).ToNot(HaveOccurred())

	waitForConfigToBePropagated(kv.ResourceVersion)
	log.DefaultLogger().Infof("system is in sync with kubevirt config resource version %s", kv.ResourceVersion)

	return kv
}

func UpdateCDIConfigMap(cdiConfig *k8sv1.ConfigMap) *k8sv1.ConfigMap {
	if cdiConfig == nil {
		return nil
	}

	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	currentConfig, err := virtClient.CoreV1().ConfigMaps(flags.ContainerizedDataImporterNamespace).Get(context.Background(), cdiConfig.Name, metav1.GetOptions{})
	util2.PanicOnError(err)
	old, err := json.Marshal(currentConfig)
	Expect(err).ToNot(HaveOccurred())

	if reflect.DeepEqual(currentConfig.Data, cdiConfig.Data) {
		return currentConfig
	}

	if config.GinkgoConfig.ParallelTotal > 1 {
		Fail("Tests which alter the global CDI configuration must not be executed in parallel")
	}

	updatedConfig := currentConfig.DeepCopy()
	updatedConfig.Data = cdiConfig.Data
	newJson, err := json.Marshal(updatedConfig)
	Expect(err).ToNot(HaveOccurred())

	patch, err := strategicpatch.CreateTwoWayMergePatch(old, newJson, currentConfig)
	Expect(err).ToNot(HaveOccurred())

	currentConfig, err = virtClient.CoreV1().ConfigMaps(flags.ContainerizedDataImporterNamespace).Patch(context.Background(), currentConfig.GetName(), types.MergePatchType, patch, metav1.PatchOptions{})
	Expect(err).ToNot(HaveOccurred())
	return currentConfig
}

// resetToDefaultConfig resets the config to the state found when the test suite started. It will wait for the config to
// be propagated to all components before it returns. It will only update the configuration and wait for it to be
// propagated if the current config in use does not match the original one.
func resetToDefaultConfig() {
	if config.GinkgoConfig.ParallelTotal > 1 {
		// Tests which alter the global kubevirt config must be run serial, therefor, if we run in parallel
		// we can just skip the restore step.
		return
	}

	UpdateKubeVirtConfigValueAndWait(KubeVirtDefaultConfig)
	UpdateCDIConfigMap(CDIInsecureRegistryConfig)
}

type compare func(string, string) bool

func ExpectResourceVersionToBeLessThanConfigVersion(resourceVersion, configVersion string) bool {
	rv, err := strconv.ParseInt(resourceVersion, 10, 32)
	if err != nil {
		log.DefaultLogger().Reason(err).Errorf("Resource version is unable to be parsed")
		return false
	}

	crv, err := strconv.ParseInt(configVersion, 10, 32)
	if err != nil {
		log.DefaultLogger().Reason(err).Errorf("Config resource version is unable to be parsed")
		return false
	}

	if rv > crv {
		log.DefaultLogger().Errorf("Config is not in sync. Expected %s or greater, Got %s", resourceVersion, configVersion)
		return false
	}

	return true
}

func ExpectResourceVersionToBeEqualConfigVersion(resourceVersion, configVersion string) bool {
	if resourceVersion > configVersion {
		log.DefaultLogger().Errorf("Config is not in sync. Expected %s, Got %s", resourceVersion, configVersion)
		return false
	}

	return true
}

func waitForConfigToBePropagated(resourceVersion string) {
	WaitForConfigToBePropagatedToComponent("kubevirt.io=virt-controller", resourceVersion, ExpectResourceVersionToBeLessThanConfigVersion)
	WaitForConfigToBePropagatedToComponent("kubevirt.io=virt-api", resourceVersion, ExpectResourceVersionToBeLessThanConfigVersion)
	WaitForConfigToBePropagatedToComponent("kubevirt.io=virt-handler", resourceVersion, ExpectResourceVersionToBeLessThanConfigVersion)
}

func WaitForConfigToBePropagatedToComponent(podLabel string, resourceVersion string, compareResourceVersions compare) {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	errComponentInfo := fmt.Sprintf("component: \"%s\"", strings.TrimPrefix(podLabel, "kubevirt.io="))

	EventuallyWithOffset(3, func() error {
		pods, err := virtClient.CoreV1().Pods(flags.KubeVirtInstallNamespace).List(context.Background(), metav1.ListOptions{LabelSelector: podLabel})

		if err != nil {
			return fmt.Errorf("failed to fetch pods. %s", errComponentInfo)
		}
		for _, pod := range pods.Items {
			errAdditionalInfo := errComponentInfo + fmt.Sprintf(", pod: \"%s\"", pod.Name)

			if pod.DeletionTimestamp != nil {
				continue
			}
			body, err := CallUrlOnPod(&pod, "8443", "/healthz")
			if err != nil {
				return fmt.Errorf("failed to call healthz endpoint. %s", errAdditionalInfo)
			}
			result := map[string]interface{}{}
			err = json.Unmarshal(body, &result)
			if err != nil {
				return fmt.Errorf("failed to parse response from healthz endpoint. %s", errAdditionalInfo)
			}

			if configVersion := result["config-resource-version"].(string); !compareResourceVersions(resourceVersion, configVersion) {
				return fmt.Errorf("resource & config versions (%s and %s respectively) are not as expected. %s ",
					resourceVersion, configVersion, errAdditionalInfo)
			}
		}
		return nil
	}, 10*time.Second, 1*time.Second).ShouldNot(HaveOccurred())
}

func WaitAgentConnected(virtClient kubecli.KubevirtClient, vmi *v1.VirtualMachineInstance) {
	WaitForVMICondition(virtClient, vmi, v1.VirtualMachineInstanceAgentConnected, 12*60)
}

func WaitForVMICondition(virtClient kubecli.KubevirtClient, vmi *v1.VirtualMachineInstance, conditionType v1.VirtualMachineInstanceConditionType, timeoutSec int) {
	By(fmt.Sprintf("Waiting for %s condition", conditionType))
	EventuallyWithOffset(1, func() bool {
		updatedVmi, err := virtClient.VirtualMachineInstance(util2.NamespaceTestDefault).Get(vmi.Name, &metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		for _, condition := range updatedVmi.Status.Conditions {
			if condition.Type == conditionType && condition.Status == k8sv1.ConditionTrue {
				return true
			}
		}
		return false
	}, time.Duration(timeoutSec)*time.Second, 2).Should(BeTrue(), fmt.Sprintf("Should have %s condition", conditionType))
}

func WaitForVMIConditionRemovedOrFalse(virtClient kubecli.KubevirtClient, vmi *v1.VirtualMachineInstance, conditionType v1.VirtualMachineInstanceConditionType, timeoutSec int) {
	By(fmt.Sprintf("Waiting for %s condition removed or false", conditionType))
	EventuallyWithOffset(1, func() bool {
		updatedVmi, err := virtClient.VirtualMachineInstance(util2.NamespaceTestDefault).Get(vmi.Name, &metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		for _, condition := range updatedVmi.Status.Conditions {
			if condition.Type == conditionType && condition.Status == k8sv1.ConditionTrue {
				return true
			}
		}
		return false
	}, time.Duration(timeoutSec)*time.Second, 2).Should(BeFalse(), fmt.Sprintf("Should have no or false %s condition", conditionType))
}

func WaitForVMCondition(virtClient kubecli.KubevirtClient, vm *v1.VirtualMachine, conditionType v1.VirtualMachineConditionType, timeoutSec int) {
	By(fmt.Sprintf("Waiting for %s condition", conditionType))
	EventuallyWithOffset(1, func() bool {
		updatedVm, err := virtClient.VirtualMachine(util2.NamespaceTestDefault).Get(vm.Name, &metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		for _, condition := range updatedVm.Status.Conditions {
			if condition.Type == conditionType && condition.Status == k8sv1.ConditionTrue {
				return true
			}
		}
		return false
	}, time.Duration(timeoutSec)*time.Second, 2).Should(BeTrue(), fmt.Sprintf("Should have %s condition", conditionType))
}

func WaitForVMConditionRemovedOrFalse(virtClient kubecli.KubevirtClient, vm *v1.VirtualMachine, conditionType v1.VirtualMachineConditionType, timeoutSec int) {
	By(fmt.Sprintf("Waiting for %s condition removed or false", conditionType))
	EventuallyWithOffset(1, func() bool {
		updatedVm, err := virtClient.VirtualMachine(util2.NamespaceTestDefault).Get(vm.Name, &metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		for _, condition := range updatedVm.Status.Conditions {
			if condition.Type == conditionType && condition.Status == k8sv1.ConditionTrue {
				return true
			}
		}
		return false
	}, time.Duration(timeoutSec)*time.Second, 2).Should(BeFalse(), fmt.Sprintf("Should have no or false %s condition", conditionType))
}

// GeneratePrivateKey creates a RSA Private Key of specified byte size
func GeneratePrivateKey(bitSize int) (*rsa.PrivateKey, error) {
	privateKey, err := rsa.GenerateKey(cryptorand.Reader, bitSize)
	if err != nil {
		return nil, err
	}

	err = privateKey.Validate()
	if err != nil {
		return nil, err
	}

	return privateKey, nil
}

// GeneratePublicKey will return in the format "ssh-rsa ..."
func GeneratePublicKey(privatekey *rsa.PublicKey) ([]byte, error) {
	publicRsaKey, err := ssh.NewPublicKey(privatekey)
	if err != nil {
		return nil, err
	}

	publicKeyBytes := ssh.MarshalAuthorizedKey(publicRsaKey)

	return publicKeyBytes, nil
}

// EncodePrivateKeyToPEM encodes Private Key from RSA to PEM format
func EncodePrivateKeyToPEM(privateKey *rsa.PrivateKey) []byte {
	// Get ASN.1 DER format
	privDER := x509.MarshalPKCS1PrivateKey(privateKey)

	privateBlock := pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   privDER,
	}

	// Private key in PEM format
	privatePEM := pem.EncodeToMemory(&privateBlock)

	return privatePEM
}

func PodReady(pod *k8sv1.Pod) k8sv1.ConditionStatus {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == k8sv1.PodReady {
			return cond.Status
		}
	}
	return k8sv1.ConditionFalse
}

func RetryWithMetadataIfModified(objectMeta metav1.ObjectMeta, do func(objectMeta metav1.ObjectMeta) error) (err error) {
	return RetryIfModified(func() error {
		return do(objectMeta)
	})
}

func RetryIfModified(do func() error) (err error) {
	retries := 0
	for err = do(); errors.IsConflict(err); err = do() {
		if retries >= 10 {
			return fmt.Errorf("object seems to be permanently modified, failing after 10 retries: %v", err)
		}
		retries++
		log.DefaultLogger().Reason(err).Infof("Object got modified, will retry.")
	}
	return err
}

func GenerateRandomMac() (net.HardwareAddr, error) {
	prefix := []byte{0x02, 0x00, 0x00} // local unicast prefix
	suffix := make([]byte, 3)
	_, err := cryptorand.Read(suffix)
	if err != nil {
		return nil, err
	}
	return net.HardwareAddr(append(prefix, suffix...)), nil
}

func GetUrl(urlIndex int) string {
	var str string

	switch urlIndex {
	case AlpineHttpUrl:
		str = fmt.Sprintf("http://cdi-http-import-server.%s/images/alpine.iso", flags.KubeVirtInstallNamespace)
	case DummyFileHttpUrl:
		str = fmt.Sprintf("http://cdi-http-import-server.%s/dummy.file", flags.KubeVirtInstallNamespace)
	case CirrosHttpUrl:
		str = fmt.Sprintf("http://cdi-http-import-server.%s/images/cirros.img", flags.KubeVirtInstallNamespace)
	case VirtWhatCpuidHelperHttpUrl:
		str = fmt.Sprintf("http://cdi-http-import-server.%s/virt-what-cpuid-helper", flags.KubeVirtInstallNamespace)
	default:
		str = ""
	}

	return str
}

func getCert(pod *k8sv1.Pod, port string) []byte {
	randPort := strconv.Itoa(int(4321 + rand.Intn(6000)))
	var rawCert []byte
	mutex := &sync.Mutex{}
	conf := &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			mutex.Lock()
			defer mutex.Unlock()
			rawCert = rawCerts[0]
			return nil
		},
	}

	var cert []byte
	EventuallyWithOffset(2, func() []byte {
		stopChan := make(chan struct{})
		defer close(stopChan)
		err := ForwardPorts(pod, []string{fmt.Sprintf("%s:%s", randPort, port)}, stopChan, 10*time.Second)
		ExpectWithOffset(2, err).ToNot(HaveOccurred())

		conn, err := tls.Dial("tcp4", fmt.Sprintf("localhost:%s", randPort), conf)
		if err == nil {
			defer conn.Close()
		}
		mutex.Lock()
		defer mutex.Unlock()
		cert = make([]byte, len(rawCert))
		copy(cert, rawCert)
		return cert
	}, 40*time.Second, 1*time.Second).Should(Not(BeEmpty()))

	return cert
}

func CallUrlOnPod(pod *k8sv1.Pod, port string, url string) ([]byte, error) {
	randPort := strconv.Itoa(int(4321 + rand.Intn(6000)))
	stopChan := make(chan struct{})
	defer close(stopChan)
	err := ForwardPorts(pod, []string{fmt.Sprintf("%s:%s", randPort, port)}, stopChan, 10*time.Second)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, VerifyPeerCertificate: func(_ [][]byte, _ [][]*x509.Certificate) error {
			return nil
		}},
	}
	client := &http.Client{Transport: tr}
	resp, err := client.Get(fmt.Sprintf("https://localhost:%s/%s", randPort, strings.TrimSuffix(url, "/")))
	if err != nil {
		return nil, err
	}
	return ioutil.ReadAll(resp.Body)
}

// GetCertsForPods returns the used certificates for all pods matching  the label selector
func GetCertsForPods(labelSelector string, namespace string, port string) ([][]byte, error) {
	cli, err := kubecli.GetKubevirtClient()
	Expect(err).ToNot(HaveOccurred())
	pods, err := cli.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
	Expect(err).ToNot(HaveOccurred())
	Expect(pods.Items).ToNot(BeEmpty())

	var certs [][]byte

	for _, pod := range pods.Items {
		err := func() error {
			certs = append(certs, getCert(&pod, port))
			return nil
		}()
		if err != nil {
			return nil, err
		}
	}
	return certs, nil
}

// EnsurePodsCertIsSynced waits until new certificates are rolled out  to all pods which are matching the specified labelselector.
// Once all certificates are in sync, the final secret is returned
func EnsurePodsCertIsSynced(labelSelector string, namespace string, port string) []byte {
	var certs [][]byte
	EventuallyWithOffset(1, func() bool {
		var err error
		certs, err = GetCertsForPods(labelSelector, namespace, port)
		Expect(err).ToNot(HaveOccurred())
		if len(certs) == 0 {
			return true
		}
		for _, crt := range certs {
			if !reflect.DeepEqual(certs[0], crt) {
				return false
			}
		}
		return true
	}, 90*time.Second, 1*time.Second).Should(BeTrue(), "certificates across '%s' pods are not in sync", labelSelector)
	if len(certs) > 0 {
		return certs[0]
	}
	return nil
}

// GetPodsCertIfSynced returns the certificate for all matching pods once all of them use the same certificate
func GetPodsCertIfSynced(labelSelector string, namespace string, port string) (cert []byte, synced bool, err error) {
	certs, err := GetCertsForPods(labelSelector, namespace, port)
	if err != nil {
		return nil, false, err
	}
	if len(certs) == 0 {
		return nil, true, nil
	}
	for _, crt := range certs {
		if !reflect.DeepEqual(certs[0], crt) {
			return nil, false, nil
		}
	}
	return certs[0], true, nil
}

func GetCertFromSecret(secretName string) []byte {
	virtClient, err := kubecli.GetKubevirtClient()
	Expect(err).ToNot(HaveOccurred())
	secret, err := virtClient.CoreV1().Secrets(flags.KubeVirtInstallNamespace).Get(context.Background(), secretName, metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred())
	if rawBundle, ok := secret.Data[bootstrap.CertBytesValue]; ok {
		return rawBundle
	}
	return nil
}

func GetBundleFromConfigMap(configMapName string) ([]byte, []*x509.Certificate) {
	virtClient, err := kubecli.GetKubevirtClient()
	Expect(err).ToNot(HaveOccurred())
	configMap, err := virtClient.CoreV1().ConfigMaps(flags.KubeVirtInstallNamespace).Get(context.Background(), configMapName, metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred())
	if rawBundle, ok := configMap.Data[components.CABundleKey]; ok {
		crts, err := cert.ParseCertsPEM([]byte(rawBundle))
		Expect(err).ToNot(HaveOccurred())
		return []byte(rawBundle), crts
	}
	return nil, nil
}

func ContainsCrt(bundle []byte, containedCrt []byte) bool {
	crts, err := cert.ParseCertsPEM(bundle)
	Expect(err).ToNot(HaveOccurred())
	attached := false
	for _, crt := range crts {
		crtBytes := cert.EncodeCertPEM(crt)
		if reflect.DeepEqual(crtBytes, containedCrt) {
			attached = true
			break
		}
	}
	return attached
}

func FormatIPForURL(ip string) string {
	if netutils.IsIPv6String(ip) {
		return "[" + ip + "]"
	}
	return ip
}

func getClusterDnsServiceIP(virtClient kubecli.KubevirtClient) (string, error) {
	dnsServiceName := "kube-dns"
	dnsNamespace := "kube-system"
	if IsOpenShift() {
		dnsServiceName = "dns-default"
		dnsNamespace = "openshift-dns"
	}
	kubeDNSService, err := virtClient.CoreV1().Services(dnsNamespace).Get(context.Background(), dnsServiceName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return kubeDNSService.Spec.ClusterIP, nil
}

func GetKubernetesApiServiceIp(virtClient kubecli.KubevirtClient) (string, error) {
	kubernetesServiceName := "kubernetes"
	kubernetesServiceNamespace := "default"

	kubernetesService, err := virtClient.CoreV1().Services(kubernetesServiceNamespace).Get(context.Background(), kubernetesServiceName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return kubernetesService.Spec.ClusterIP, nil
}

func IsRunningOnKindInfra() bool {
	provider := os.Getenv("KUBEVIRT_PROVIDER")
	return strings.HasPrefix(provider, "kind")
}

func IsUsingBuiltinNodeDrainKey() bool {
	return GetNodeDrainKey() == "node.kubernetes.io/unschedulable"
}

func GetNodeDrainKey() string {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	kv := util2.GetCurrentKv(virtClient)
	if kv.Spec.Configuration.MigrationConfiguration != nil && kv.Spec.Configuration.MigrationConfiguration.NodeDrainTaintKey != nil {
		return *kv.Spec.Configuration.MigrationConfiguration.NodeDrainTaintKey
	}

	return virtconfig.NodeDrainTaintDefaultKey
}

func GetKubeVirtConfigMap() (*k8sv1.ConfigMap, error) {
	virtClient, err := kubecli.GetKubevirtClient()
	util2.PanicOnError(err)

	options := metav1.GetOptions{}
	cfgMap, err := virtClient.CoreV1().ConfigMaps(flags.KubeVirtInstallNamespace).Get(context.Background(), virtconfig.ConfigMapName, options)
	if err != nil && !errors.IsNotFound(err) {
		util2.PanicOnError(err)
	}

	return cfgMap, err
}

func RandTmpDir() string {
	return tmpPath + "/" + rand.String(10)
}

func getTagHint() string {
	//git describe --tags --abbrev=0 "$(git rev-parse HEAD)"
	cmd := exec.Command("git", "rev-parse", "HEAD")
	bytes, err := cmd.Output()
	if err != nil {
		return ""
	}

	cmd = exec.Command("git", "describe", "--tags", "--abbrev=0", strings.TrimSpace(string(bytes)))
	bytes, err = cmd.Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(strings.Split(string(bytes), "-rc")[0])

}

func DetectLatestUpstreamOfficialTag() (string, error) {
	client := github.NewClient(nil)

	var err error
	var releases []*github.RepositoryRelease

	Eventually(func() error {
		releases, _, err = client.Repositories.ListReleases(context.Background(), "kubevirt", "kubevirt", &github.ListOptions{PerPage: 10000})

		return err
	}, 10*time.Second, 1*time.Second).ShouldNot(HaveOccurred())

	var vs []*semver.Version

	for _, release := range releases {
		if *release.Draft ||
			*release.Prerelease ||
			len(release.Assets) == 0 {

			continue
		}
		v, err := semver.NewVersion(*release.TagName)
		if err != nil {
			panic(err)
		}
		vs = append(vs, v)
	}

	if len(vs) == 0 {
		return "", fmt.Errorf("No kubevirt releases found")
	}

	// decending order from most recent.
	sort.Sort(sort.Reverse(semver.Collection(vs)))

	// most recent tag
	tag := fmt.Sprintf("v%v", vs[0])

	// tag hint gives us information about the most recent tag in the current branch
	// this is executing in. We want to make sure we are using the previous most
	// recent official release from the branch we're in if possible. Note that this is
	// all best effort. If a tag hint can't be detected, we move on with the most
	// recent release from master.
	tagHint := getTagHint()
	hint, err := semver.NewVersion(tagHint)

	if tagHint != "" && err == nil {
		for _, v := range vs {
			if v.LessThan(hint) || v.Equal(hint) {
				tag = fmt.Sprintf("v%v", v)
				By(fmt.Sprintf("Choosing tag %s influenced by tag hint %s", tag, tagHint))
				break
			}
		}
	}

	By(fmt.Sprintf("By detecting latest upstream official tag %s for current branch", tag))
	return tag, nil
}

func IsLauncherCapabilityValid(capability k8sv1.Capability) bool {
	switch capability {
	case
		capNetBindService,
		capSysNice:
		return true
	}
	return false
}

func IsLauncherCapabilityDropped(capability k8sv1.Capability) bool {
	switch capability {
	case
		capNetRaw:
		return true
	}
	return false
}

// VMILauncherIgnoreWarnings waiting for the VMI to be up but ignoring warnings like a disconnected guest-agent
func VMILauncherIgnoreWarnings(virtClient kubecli.KubevirtClient) func(vmi *v1.VirtualMachineInstance) *v1.VirtualMachineInstance {
	return func(vmi *v1.VirtualMachineInstance) *v1.VirtualMachineInstance {
		By("Starting a VirtualMachineInstance")
		obj, err := virtClient.RestClient().Post().Resource("virtualmachineinstances").Namespace(util2.NamespaceTestDefault).Body(vmi).Do(context.Background()).Get()
		Expect(err).To(BeNil())

		By("Waiting the VirtualMachineInstance start")
		vmi, ok := obj.(*v1.VirtualMachineInstance)
		Expect(ok).To(BeTrue(), "Object is not of type *v1.VirtualMachineInstance")
		// Warnings are okay. We'll receive a warning that the agent isn't connected
		// during bootup, but that is transient
		Expect(WaitForSuccessfulVMIStartIgnoreWarnings(obj)).ToNot(BeEmpty())
		return vmi
	}
}
