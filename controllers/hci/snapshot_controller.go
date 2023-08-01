package hci

import (
	"context"
	"encoding/json"
	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	_ "github.com/longhorn/backupstore/nfs"
	_ "github.com/longhorn/backupstore/s3"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type VolumeSnapshotReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	VmBackupReconciler *VirtualMachineBackupReconciler
}

//+kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshotclasses,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshotcontents,verbs=get;list;watch;create;update;delete

func (v *VolumeSnapshotReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log.Log.Info("get volumesnapshot: " + req.String())
	snapshot := &snapshotv1.VolumeSnapshot{}
	if err := v.Get(ctx, req.NamespacedName, snapshot); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	bs, _ := json.Marshal(snapshot)
	log.Log.Info("update volumesnapshot: " + req.String() + ": " + string(bs))
	_, err := v.VmBackupReconciler.updateVolumeSnapshotChanged(req.String(), snapshot)
	return ctrl.Result{}, err
}

func (v *VolumeSnapshotReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&snapshotv1.VolumeSnapshot{}).
		Complete(v)
}
