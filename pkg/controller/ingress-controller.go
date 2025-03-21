package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	cloudflarecontroller "github.com/oliverbaehler/cloudflare-tunnel-ingress-controller/pkg/cloudflare-controller"
	"github.com/oliverbaehler/cloudflare-tunnel-ingress-controller/pkg/exposure"
	"github.com/pkg/errors"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// IngressController should implement the Reconciler interface
var _ reconcile.Reconciler = &IngressController{}

const WellKnownIngressAnnotation = "kubernetes.io/ingress.class"

type IngressController struct {
	logger              logr.Logger
	kubeClient          client.Client
	ingressClassName    string
	controllerClassName string
	staticBackend       string
	tunnelClient        *cloudflarecontroller.TunnelClient
}

func NewIngressController(logger logr.Logger, kubeClient client.Client, ingressClassName string, controllerClassName string, tunnelClient *cloudflarecontroller.TunnelClient, backend string) *IngressController {
	return &IngressController{logger: logger, kubeClient: kubeClient, ingressClassName: ingressClassName, controllerClassName: controllerClassName, tunnelClient: tunnelClient, staticBackend: backend}
}

func (i *IngressController) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	origin := networkingv1.Ingress{}
	err := i.kubeClient.Get(ctx, request.NamespacedName, &origin)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, errors.Wrapf(err, "fetch ingress %s", request.NamespacedName)
	}

	controlled, err := i.isControlledByThisController(ctx, origin)
	if err != nil && !apierrors.IsNotFound(err) {
		return reconcile.Result{}, errors.Wrapf(err, "check if ingress %s is controlled by this controller", request.NamespacedName)
	}

	if !controlled {
		i.logger.Info("ingress is NOT controlled by this controller",
			"ingress", request.NamespacedName,
			"controlled-ingress-class", i.ingressClassName,
			"controlled-controller-class", i.controllerClassName,
		)
		return reconcile.Result{
			Requeue: false,
		}, nil
	}

	i.logger.V(1).Info("ingress is controlled by this controller",
		"ingress", request.NamespacedName,
		"controlled-ingress-class", i.ingressClassName,
		"controlled-controller-class", i.controllerClassName,
	)

	i.logger.Info("update cloudflare tunnel config", "triggered-by", request.NamespacedName)

	if origin.DeletionTimestamp == nil {
		err = i.attachFinalizer(ctx, *(origin.DeepCopy()))
		if err != nil {
			return reconcile.Result{}, errors.Wrapf(err, "attach finalizer to ingress %s", request.NamespacedName)
		}
	} else {
		if !i.hasFinalizer(ctx, origin) {
			i.logger.V(1).Info("ingress is being deleted and already finillized by this controller",
				"ingress", request.NamespacedName,
				"controlled-ingress-class", i.ingressClassName,
				"controlled-controller-class", i.controllerClassName,
			)
			return reconcile.Result{
				Requeue: false,
			}, nil
		}
	}

	ingresses, err := i.listControlledIngresses(ctx)
	if err != nil {
		return reconcile.Result{}, errors.Wrap(err, "list controlled ingresses")
	}

	var allExposures []exposure.Exposure
	for _, ingress := range ingresses {
		// best effort to extract exposures from all ingresses
		exposures, err := FromIngressToExposure(ctx, i.logger, i.kubeClient, ingress, i.staticBackend)
		if err != nil {
			i.logger.Info("extract exposures from ingress, skipped", "triggered-by", request.NamespacedName, "ingress", fmt.Sprintf("%s/%s", ingress.Namespace, ingress.Name), "error", err)
		}
		allExposures = append(allExposures, exposures...)
	}
	i.logger.V(3).Info("all exposures", "exposures", allExposures)

	err = i.tunnelClient.PutExposures(ctx, allExposures)
	if err != nil {
		return reconcile.Result{}, errors.Wrap(err, "put exposures")
	}

	if origin.DeletionTimestamp != nil {
		err = i.cleanFinalizer(ctx, origin)
		if err != nil {
			return reconcile.Result{}, errors.Wrapf(err, "clean finalizer from ingress %s", request.NamespacedName)
		}
	}

	i.logger.V(3).Info("reconcile completed", "triggered-by", request.NamespacedName)
	return reconcile.Result{}, nil
}

func (i *IngressController) isControlledByThisController(ctx context.Context, target networkingv1.Ingress) (bool, error) {
	if i.ingressClassName == target.GetAnnotations()[WellKnownIngressAnnotation] {
		return true, nil
	}

	if target.Spec.IngressClassName == nil {
		return false, nil
	}

	controlledIngressClassNames, err := i.listControlledIngressClasses(ctx)
	if err != nil {
		return false, errors.Wrapf(err, "fetch controlled ingress classes with controller name %s", i.controllerClassName)
	}

	if stringSliceContains(controlledIngressClassNames, *target.Spec.IngressClassName) {
		return true, nil
	}

	return false, nil
}

func (i *IngressController) listControlledIngressClasses(ctx context.Context) ([]string, error) {
	list := networkingv1.IngressClassList{}
	err := i.kubeClient.List(ctx, &list)
	if err != nil {
		return nil, errors.Wrap(err, "list ingress classes")
	}

	var controlledNames []string
	for _, ingressClass := range list.Items {
		// Check if the IngressClass is controlled by the specified controller
		if ingressClass.Spec.Controller == i.controllerClassName {
			controlledNames = append(controlledNames, ingressClass.Name)
		}
	}

	return controlledNames, nil
}

func (i *IngressController) listControlledIngresses(ctx context.Context) ([]networkingv1.Ingress, error) {
	controlledIngressClassNames, err := i.listControlledIngressClasses(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "fetch controlled ingress classes with controller name %s", i.controllerClassName)
	}

	var result []networkingv1.Ingress
	list := networkingv1.IngressList{}
	err = i.kubeClient.List(ctx, &list)
	if err != nil {
		return nil, errors.Wrap(err, "list ingresses")
	}

	for _, ingress := range list.Items {
		func() {
			if i.ingressClassName == ingress.GetAnnotations()[WellKnownIngressAnnotation] {
				result = append(result, ingress)
				return
			}

			if ingress.Spec.IngressClassName == nil {
				return
			}

			if stringSliceContains(controlledIngressClassNames, *ingress.Spec.IngressClassName) {
				result = append(result, ingress)
				return
			}
		}()
	}

	return result, nil
}

func removeStringFromSlice(finalizers []string, finalizer string) []string {
	var result []string
	for _, f := range finalizers {
		if f != finalizer {
			result = append(result, f)
		}
	}
	return result
}

func stringSliceContains(slice []string, element string) bool {
	for _, sliceElement := range slice {
		if sliceElement == element {
			return true
		}
	}
	return false
}
