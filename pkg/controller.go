package pkg

import (
	"context"
	v12 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v13 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	informer "k8s.io/client-go/informers/core/v1"
	netInformer "k8s.io/client-go/informers/networking/v1"
	"k8s.io/client-go/kubernetes"
	coreLister "k8s.io/client-go/listers/core/v1"
	v1 "k8s.io/client-go/listers/networking/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"log"
	"reflect"
	"time"
)

const (
	workNum  = 5
	maxRetry = 3
)

type controller struct {
	client        kubernetes.Interface
	ingressLister v1.IngressLister
	serviceLister coreLister.ServiceLister
	queue         workqueue.RateLimitingInterface
}

func (c *controller) updateService(oldObj interface{}, newObj interface{}) {

	if reflect.DeepEqual(oldObj, newObj) {
		return
	}
	key, _ := cache.MetaNamespaceIndexFunc(newObj)
	log.Print("[UPDATE_SERVICE] ----------->>>>>>>>>>>>> ", key)

	c.enqueue(newObj)
}

func (c *controller) addService(obj interface{}) {

	key, _ := cache.MetaNamespaceIndexFunc(obj)
	log.Print("[ADD_SERVICE] ----------->>>>>>>>>>>>> ", key)

	c.enqueue(obj)
}

func (c *controller) enqueue(obj interface{}) {

	c.queue.Add(obj)
}

func (c *controller) deleteIngress(obj interface{}) {
	ingress := obj.(*v12.Ingress)
	service := v13.GetControllerOf(ingress)

	if service != nil {
		return
	}

	if service.Kind != "Service" {
		return
	}

	c.queue.Add(ingress.Namespace + "/" + ingress.Name)
}

func (c *controller) Run(stopCh chan struct{}) {
	for i := 0; i < workNum; i++ {
		go wait.Until(c.worker, time.Minute, stopCh)
	}
	<-stopCh
}

func (c *controller) worker() {
	for c.processNextItem() {

	}
}

func (c *controller) processNextItem() bool {
	item, shutdown := c.queue.Get()

	if shutdown {
		return false
	}

	defer c.queue.Done(item)

	//key := item.(string)
	err := c.syncService(item)

	if err != nil {
		c.handleError(item, err)
	}

	return true
}

func (c *controller) syncService(obj interface{}) error {
	key, err := cache.MetaNamespaceIndexFunc(obj)
	log.Print("[SYNC_SERVICE] ------->>>>>>>>>>>>>>> key:{} ", key)
	if err != nil {
		return err
	}

	namespaceKey, name, err := cache.SplitMetaNamespaceKey(key[0])

	log.Print("[SYNC_SERVICE] ------->>>>>>>>>>>>>>> namespaceKey:{} ", namespaceKey)
	log.Print("[SYNC_SERVICE] ------->>>>>>>>>>>>>>> name:{} ", name)

	if err != nil {
		return err
	}

	// delete
	service, err := c.serviceLister.Services(namespaceKey).Get(name)
	if errors.IsNotFound(err) {
		return nil
	}

	if err != nil {
		return err
	}

	// add and delete
	keyMap, ok := service.GetAnnotations()["ingress/http"]
	log.Print("[ANNOTATIONS] ------->>>>>>>>>>>>>>> ", keyMap)

	// name modify
	ingress, err := c.ingressLister.Ingresses(namespaceKey).Get(name)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	if ok && errors.IsNotFound(err) {
		// create ingress
		ig := c.constructIngress(namespaceKey, name)
		_, err := c.client.NetworkingV1().Ingresses(namespaceKey).Create(context.TODO(), ig, v13.CreateOptions{})
		if err != nil {
			return err
		}
	} else if !ok && ingress != nil {
		// delete ingress
		err := c.client.NetworkingV1().Ingresses(namespaceKey).Delete(context.TODO(), name, v13.DeleteOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *controller) handleError(obj interface{}, err error) {
	key, err := cache.MetaNamespaceIndexFunc(obj)
	if err != nil {
		return
	}
	log.Print("[HANDLE_NAMESPACE] ----------->>>>>>>>>>>>> ", key)
	if c.queue.NumRequeues(key) <= maxRetry {
		c.queue.AddRateLimited(key)
		return
	}

	runtime.HandleError(err)

	c.queue.Forget(key)
}

func (c *controller) constructIngress(namespaceKey string, name string) *v12.Ingress {
	ingress := v12.Ingress{}

	ingress.Name = name
	ingress.Namespace = namespaceKey
	pathType := v12.PathTypePrefix

	ingress.Spec = v12.IngressSpec{
		Rules: []v12.IngressRule{
			{
				Host: "example.com",
				IngressRuleValue: v12.IngressRuleValue{
					HTTP: &v12.HTTPIngressRuleValue{
						Paths: []v12.HTTPIngressPath{
							{
								Path:     "/",
								PathType: &pathType,
								Backend: v12.IngressBackend{
									Service: &v12.IngressServiceBackend{
										Name: name,
										Port: v12.ServiceBackendPort{
											Number: 80,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	return &ingress
}

func NewController(client kubernetes.Interface, serviceInformer informer.ServiceInformer, ingressInformer netInformer.IngressInformer) controller {
	c := controller{
		client:        client,
		ingressLister: ingressInformer.Lister(),
		serviceLister: serviceInformer.Lister(),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "ingressManager"),
	}

	serviceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addService,
		UpdateFunc: c.updateService,
	})

	ingressInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: c.deleteIngress,
	})

	return c
}
