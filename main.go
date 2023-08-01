/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"
	"git.netfuse.cn/algo/hci-controller-manager/pkg/indexer"
	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	lhv1beta1 "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta1"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"net/http"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	hciv1beta1 "git.netfuse.cn/algo/hci-controller-manager/apis/hci/v1beta1"
	hcicontrollers "git.netfuse.cn/algo/hci-controller-manager/controllers/hci"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	Codecs         = serializer.NewCodecFactory(scheme)
	ParameterCodec = runtime.NewParameterCodec(scheme)
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(hciv1beta1.AddToScheme(scheme))

	utilruntime.Must(snapshotv1.AddToScheme(scheme))

	utilruntime.Must(lhv1beta1.AddToScheme(scheme))

	utilruntime.Must(kubevirtv1.AddToScheme(scheme))

	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "ab6d3bc0.wjyl.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	copyConfig := rest.CopyConfig(ctrl.GetConfigOrDie())
	copyConfig.GroupVersion = &k8sschema.GroupVersion{Group: kubevirtv1.SubresourceGroupName, Version: kubevirtv1.ApiLatestVersion}
	copyConfig.APIPath = "/apis"
	copyConfig.NegotiatedSerializer = Codecs.WithoutConversion()
	restClient, err := rest.RESTClientFor(copyConfig)
	if err != nil {
		setupLog.Error(err, "unable to set rest client")
		os.Exit(1)
	}

	if err = (&hcicontrollers.VirtualMachineImageReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),

		HttpClient: http.Client{
			Timeout: 15 * time.Second,
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "VirtualMachineImage")
		os.Exit(1)
	}
	if err = (&hcicontrollers.BackingImageReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "BackingImage")
		os.Exit(1)
	}

	var vmBackupReconciler = &hcicontrollers.VirtualMachineBackupReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("virtualmachinebackup-controller"),
	}
	if err = vmBackupReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "VirtualMachineBackup")
		os.Exit(1)
	}
	if err = (&hcicontrollers.MetadataReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Setting")
		os.Exit(1)
	}
	if err = (&hcicontrollers.BackupSettingReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Setting")
		os.Exit(1)
	}
	if err = (&hcicontrollers.LonghornBackupReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		VmBackupReconciler: vmBackupReconciler,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Backup")
		os.Exit(1)
	}
	if err = (&hcicontrollers.VolumeSnapshotReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		VmBackupReconciler: vmBackupReconciler,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "VolumeSnapshot")
		os.Exit(1)
	}

	var vmRestoreReconciler = &hcicontrollers.VirtualMachineRestoreReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Recorder:   mgr.GetEventRecorderFor("virtualmachinerestore-controller"),
		RestClient: restClient,
	}
	if err = vmRestoreReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "VirtualMachineRestore")
		os.Exit(1)
	}

	var pVCReconciler = &hcicontrollers.PVCReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		VmRestoreReconciler: vmRestoreReconciler,
		PersistentVolumeClaimCache: &indexer.PersistentVolumeClaimCache{
			Indexer: cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
				indexer.PVCByVMIndex: indexer.AnnotationsIndexFunc,
			}),
		},
	}

	if err = pVCReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PersistentVolumeClaim")
		os.Exit(1)
	}
	if err = (&hcicontrollers.KeyPairReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KeyPair")
		os.Exit(1)
	}

	var vmReconciler = &hcicontrollers.VMReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		VmRestoreReconciler: vmRestoreReconciler,
		PVCReconciler:       *pVCReconciler,
	}
	if err = vmReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "VM")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
