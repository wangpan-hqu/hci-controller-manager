package hci

import (
	"context"
	_ "github.com/longhorn/backupstore/nfs"
	_ "github.com/longhorn/backupstore/s3"
	"github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type LonghornBackupReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	VmBackupReconciler *VirtualMachineBackupReconciler
}

//+kubebuilder:rbac:groups=longhorn.io,resources=volumes,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=longhorn.io,resources=backups,verbs=get;list;watch;create;update;delete

func (r *LonghornBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	lhbackup := &v1beta1.Backup{}
	if err := r.Get(ctx, req.NamespacedName, lhbackup); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	_, err := r.VmBackupReconciler.OnLHBackupChanged(req.String(), lhbackup)
	return ctrl.Result{}, err
}

func (l *LonghornBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.Backup{}).
		Complete(l)
}
