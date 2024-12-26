/*
Copyright 2024.

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

package controller

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"time"

	// crdv1alpha1 "github.com/soub4i/kubeststus-operator/api/v1alpha1"
	crdv1 "github.com/soub4i/kubeststus-operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// KubeStatusReconciler reconciles a KubeStatus object
type KubeStatusReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

type Endpoint struct {
	Name         string
	URI          string
	Protocol     string
	Port         int32
	PublicFacing bool
}

const (
	DEFAULT_CM_NAME     = "kubestatus-configmap"
	DEFAULT_DEPLOY_NAME = "kubestatus-deployment"
	DEFAULT_SVC_NAME    = "kubestatus-service"
	DEFAULT_SECRET_NAME = "kubestatus-auth-secret"
	DEFAULT_NAME        = "kubestatus-operator"
	DEFAULT_NS          = "kubestatus"
	DEFAULT_SVC_LABEL   = "kubestatus/watch"
)

// Existing markers
// +kubebuilder:rbac:groups=crd.soubai.me,resources=kubestatuses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=crd.soubai.me,resources=kubestatuses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=crd.soubai.me,resources=kubestatuses/finalizers,verbs=update
// +kubebuilder:rbac:groups=crd.soubai.me,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the KubeStatus object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *KubeStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	ks := crdv1.KubeStatus{}
	if req.NamespacedName.Name == "" {
		req.NamespacedName.Name = DEFAULT_NAME
	}
	if req.NamespacedName.Namespace == "" {
		req.NamespacedName.Namespace = DEFAULT_NS
	}

	err := r.Get(ctx, req.NamespacedName, &ks)

	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Error(err, "Kubestatus resource not found. Ignoring ...")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get Kubestatus")
		return ctrl.Result{}, err
	}

	err = r.createRole(ctx)

	if err != nil {

		log.Error(err, "Kubestatus RBAC creation error. Ignoring ...")
		//	return ctrl.Result{}, err
	}

	dep := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: DEFAULT_DEPLOY_NAME, Namespace: DEFAULT_NS}, dep)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.deploymentForKS(&ks)
		log.Info("Creating a new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to create new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
			return ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	}

	cm := &corev1.ConfigMap{}
	cmname := ks.Spec.ConfigMapName
	if cmname == "" {
		cmname = DEFAULT_CM_NAME
	}

	err = r.Get(ctx, types.NamespacedName{Name: cmname, Namespace: DEFAULT_NS}, cm)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		cm := r.cmForKS(ctx, &ks)
		log.Info("Creating a new ConfigMap", "ConfigMap.Namespace", DEFAULT_NS, "ConfigMap.Name", cm.Name)
		err = r.Create(ctx, cm)
		if err != nil {
			log.Error(err, "Failed to create new ConfigMap", "ConfigMap.Namespace", DEFAULT_NS, "ConfigMap.Name", cm.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get ConfigMap")
		return ctrl.Result{}, err
	}
	s := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: DEFAULT_SVC_NAME, Namespace: DEFAULT_NS}, s)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		s := r.svcForKS(ctx, &ks)
		log.Info("Creating a new Service", "Namespace", DEFAULT_NS, "Name", DEFAULT_SECRET_NAME)
		err = r.Create(ctx, s)
		if err != nil {
			log.Error(err, "Failed to create new Service", "Namespace", DEFAULT_NS, "Name", DEFAULT_SVC_NAME)
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Service")
		return ctrl.Result{}, err
	}

	secret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: DEFAULT_SECRET_NAME, Namespace: DEFAULT_NS}, secret)
	if err != nil && errors.IsNotFound(err) {
		s := r.secretForKS(ctx, &ks)
		log.Info("Creating a new Authentication scret", "Secret.Namespace", secret.Namespace, "Secret.Name", secret.Name)
		err = r.Create(ctx, s)
		if err != nil {
			log.Error(err, "Failed to create new Secret", "Secret.Namespace", cm.Namespace, "Secret.Name", cm.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	r.updateServiceList(ctx, cm, &ks)

	return ctrl.Result{}, nil
}

func (r *KubeStatusReconciler) getEndpoints(c context.Context, k *crdv1.KubeStatus) []Endpoint {
	log := log.FromContext(c)
	var endpoints []Endpoint

	var svcs corev1.ServiceList
	r.Client.List(context.Background(), &svcs, &client.ListOptions{})

	if len(svcs.Items) == 0 {
		log.Info("Skipping Empty services list")
		return []Endpoint{}
	}

	for _, s := range svcs.Items {
		if v, f := s.GetAnnotations()[DEFAULT_SVC_LABEL]; !f || v != "true" {
			continue
		}
		if !slices.Contains(k.Spec.Namespaces, s.Namespace) {
			log.Info("Skipping %s, not in watched namespaces", s.Name)
			continue
		}

		endpoints = append(endpoints, Endpoint{
			Name:         s.Name,
			URI:          fmt.Sprintf("%s.%s.svc.cluster.local", s.Name, s.Namespace),
			PublicFacing: s.Spec.Type == "LoadBalancer" && s.Spec.LoadBalancerIP != "",
			Protocol:     string(s.Spec.Ports[0].Protocol),
			Port:         s.Spec.Ports[0].Port,
		})
	}
	return endpoints
}

func (r *KubeStatusReconciler) updateServiceList(c context.Context, cm *corev1.ConfigMap, k *crdv1.KubeStatus) error {
	log := log.FromContext(c)

	d := map[string]string{}
	log.Info("Update a service list", "ConfigMap.Namespace", cm.Namespace, "ConfigMap.Name", cm.Name)

	for _, e := range r.getEndpoints(c, k) {
		d[e.Name] = fmt.Sprintf("%s|%s|%d|%v", e.Protocol, e.URI, e.Port, e.PublicFacing)
	}
	cm.Data = d

	return r.Client.Update(c, cm, &client.UpdateOptions{})
}

func (r *KubeStatusReconciler) secretForKS(c context.Context, k *crdv1.KubeStatus) *corev1.Secret {

	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DEFAULT_SECRET_NAME,
			Namespace: DEFAULT_NS,
			Annotations: map[string]string{
				"kubernetes.io/service-account.name": "kubestatus",
			},
		},
		Data: map[string][]byte{
			"user":     []byte("kubestatus"),
			"password": []byte(randomString(64)),
		},
	}
	controllerutil.SetControllerReference(k, s, r.Scheme)
	return s
}

func (r *KubeStatusReconciler) createRole(c context.Context) error {
	log := log.FromContext(c)
	// Create Role as unstructured
	r.Recorder.Event(&crdv1.KubeStatus{}, "Info", "Creating", "Create Role")

	sa := &corev1.ServiceAccount{}
	err := r.Get(c, types.NamespacedName{Name: "kubestatus", Namespace: DEFAULT_NS}, sa)
	if err != nil && errors.IsNotFound(err) {

		sa := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ServiceAccount",
				"metadata": map[string]interface{}{
					"name":      "kubestatus",
					"namespace": DEFAULT_NS,
				},
			},
		}

		err := r.Client.Create(c, sa, &client.CreateOptions{})
		if err != nil {
			log.Error(err, "Error creating sa")
			r.Recorder.Eventf(&crdv1.KubeStatus{}, "Erroe", "Creating", "Error: creating SA")
			return err
		}

	}

	role := &rbacv1.Role{}
	err = r.Get(c, types.NamespacedName{Name: "kubestatus", Namespace: DEFAULT_NS}, role)
	if err != nil && errors.IsNotFound(err) {

		role := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "rbac.authorization.k8s.io/v1",
				"kind":       "Role",
				"metadata": map[string]interface{}{
					"name":      "kubestatus",
					"namespace": DEFAULT_NS,
				},
				"rules": []interface{}{
					map[string]interface{}{
						"verbs":     []string{"get", "list", "watch"},
						"apiGroups": []string{""},
						"resources": []string{"secrets"},
					},
					map[string]interface{}{
						"verbs":     []string{"get", "list", "watch"},
						"apiGroups": []string{""},
						"resources": []string{"configmaps"},
					},
				},
			},
		}

		err := r.Client.Create(c, role, &client.CreateOptions{})
		if err != nil {
			log.Error(err, "Error creating role")
			r.Recorder.Eventf(&crdv1.KubeStatus{}, "Erroe", "Creating", "Error: creating Role")
			return err
		}

	}

	rb := &rbacv1.RoleBinding{}
	err = r.Get(c, types.NamespacedName{Name: "default-to-secrets", Namespace: DEFAULT_NS}, rb)
	if err != nil && errors.IsNotFound(err) {
		roleBinding := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "rbac.authorization.k8s.io/v1",
				"kind":       "RoleBinding",
				"metadata": map[string]interface{}{
					"name":      "default-to-secrets",
					"namespace": DEFAULT_NS,
				},
				"subjects": []interface{}{
					map[string]interface{}{
						"kind":      "ServiceAccount",
						"name":      "kubestatus",
						"namespace": DEFAULT_NS,
					},
				},
				"roleRef": map[string]interface{}{
					"kind":     "Role",
					"name":     "kubestatus",
					"apiGroup": "rbac.authorization.k8s.io",
				},
			},
		}

		err = r.Client.Create(c, roleBinding, &client.CreateOptions{})

		if err != nil {
			log.Error(err, "Error creating role binding")
			r.Recorder.Eventf(&crdv1.KubeStatus{}, "Erroe", "Creating", "Error: creating Role")
			return err
		}
	}
	log.Info("RBAC ok")
	return nil
}

func (r *KubeStatusReconciler) cmForKS(c context.Context, k *crdv1.KubeStatus) *corev1.ConfigMap {

	cmname := k.Spec.ConfigMapName
	if cmname == "" {
		cmname = DEFAULT_CM_NAME
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmname,
			Namespace: DEFAULT_NS,
		},
		Data: map[string]string{},
	}
	controllerutil.SetControllerReference(k, cm, r.Scheme)
	return cm
}

func (r *KubeStatusReconciler) svcForKS(c context.Context, k *crdv1.KubeStatus) *corev1.Service {

	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DEFAULT_SVC_NAME,
			Namespace: DEFAULT_NS,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{corev1.ServicePort{
				Protocol: "TCP",
				Port:     8080,
			}},
			Selector: map[string]string{
				"app": "kubestatus",
			},
			ClusterIP: "",
		}}
	controllerutil.SetControllerReference(k, s, r.Scheme)

	return s

}

func (r *KubeStatusReconciler) deploymentForKS(k *crdv1.KubeStatus) *appsv1.Deployment {

	replicas := int32(1)
	if k.Spec.Size != 0 {
		replicas = k.Spec.Size
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DEFAULT_DEPLOY_NAME,
			Namespace: DEFAULT_NS,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "kubestatus"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "kubestatus"},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "kubestatus",
					Containers: []corev1.Container{{
						Image:           "soubai/kubestatus-operand:latest",
						Name:            "kubestatus",
						ImagePullPolicy: "Always",
						Ports: []corev1.ContainerPort{{
							ContainerPort: 8080,
							Name:          "kubestatus",
						}},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/healthy",
									Port: intstr.FromInt32(8080),
								},
							},
							InitialDelaySeconds: 3,
							PeriodSeconds:       5,
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/ready",
									Port: intstr.FromInt32(8080),
								},
							},
							InitialDelaySeconds: 5,
							PeriodSeconds:       1,
						},
					}},
				},
			},
		},
	}
	ctrl.SetControllerReference(k, dep, r.Scheme)
	return dep
}

// SetupWithManager sets up the controller with the Manager.
func (r *KubeStatusReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&crdv1.KubeStatus{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&appsv1.Deployment{}).
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&corev1.Service{},
			handler.TypedEnqueueRequestsFromMapFunc(func(c context.Context, s *corev1.Service) []ctrl.Request {
				if val, ok := s.GetAnnotations()[DEFAULT_SVC_LABEL]; ok && val == "true" {

					return []reconcile.Request{
						{
							NamespacedName: types.NamespacedName{
								Name:      DEFAULT_NAME,
								Namespace: DEFAULT_NS,
							},
						},
					}

				}
				return []ctrl.Request{}
			})),
		).
		Complete(r)

}

func randomString(length int) string {
	rand.Seed(time.Now().UnixNano())
	b := make([]byte, length+2)
	rand.Read(b)
	return fmt.Sprintf("%x", b)[2 : length+2]
}
