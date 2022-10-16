package pkg

import (
	"context"
	apiCoreV1 "k8s.io/api/core/v1"
	netV1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	informersCoreV1 "k8s.io/client-go/informers/core/v1"
	informersNetV1 "k8s.io/client-go/informers/networking/v1"
	"k8s.io/client-go/kubernetes"
	coreV1 "k8s.io/client-go/listers/core/v1"
	v1 "k8s.io/client-go/listers/networking/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"log"
	"reflect"
	"time"
)

const (
	workNum  = 5  // 工作的节点数
	maxRetry = 10 // 最大重试次数
)

// 定义控制器
type Controller struct {
	client        kubernetes.Interface
	ingressLister v1.IngressLister
	serviceLister coreV1.ServiceLister
	queue         workqueue.RateLimitingInterface
}

// 初始化控制器
func NewController(client kubernetes.Interface, serviceInformer informersCoreV1.ServiceInformer, ingressInformer informersNetV1.IngressInformer) Controller {
	c := Controller{
		client:        client,
		ingressLister: ingressInformer.Lister(),
		serviceLister: serviceInformer.Lister(),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "ingressManager"),
	}

	// 添加事件处理函数
	serviceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addService,
		UpdateFunc: c.updateService,
	})

	ingressInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: c.deleteIngress,
	})
	return c
}

// 入队
func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
	}
	c.queue.Add(key)
}

func (c *Controller) addService(obj interface{}) {
	c.enqueue(obj)
}

func (c *Controller) updateService(oldObj, newObj interface{}) {
	// todo 比较annotation
	// 这里只是比较了对象是否相同，如果相同，直接返回
	if reflect.DeepEqual(oldObj, newObj) {
		return
	}
	c.enqueue(newObj)
}

func (c *Controller) deleteIngress(obj interface{}) {
	ingress := obj.(*netV1.Ingress)
	ownerReference := metaV1.GetControllerOf(ingress)
	if ownerReference == nil {
		return
	}

	// 判断是否为真的service
	if ownerReference.Kind != "Service" {
		return
	}

	c.queue.Add(ingress.Namespace + "/" + ingress.Name)
}

// 启动控制器，可以看到开了五个协程，真正干活的是worker
func (c *Controller) Run(stopCh chan struct{}) {
	for i := 0; i < workNum; i++ {
		go wait.Until(c.worker, time.Minute, stopCh)
	}
	<-stopCh
}

func (c *Controller) worker() {
	for c.processNextItem() {
	}
}

// 业务真正处理的地方
func (c *Controller) processNextItem() bool {
	// 获取key
	item, shutdown := c.queue.Get()
	if shutdown {
		return false
	}
	defer c.queue.Done(item)

	// 调用业务逻辑
	err := c.syncService(item.(string))
	if err != nil {
		// 对错误进行处理
		c.handlerError(item.(string), err)
		return false
	}
	return true
}

func (c *Controller) syncService(item string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(item)
	log.Print("[SYNC_SERVICE] ------->>>>>>>>>>>>>>> namespace: ", namespace)
	log.Print("[SYNC_SERVICE] ------->>>>>>>>>>>>>>> name: ", name)
	if err != nil {
		return err
	}
	// 获取service
	service, err := c.serviceLister.Services(namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// 新增和删除
	serviceAnnotations, ok := service.GetAnnotations()["ingress/http"]
	log.Print("[SYNC_SERVICE] ------->>>>>>>>>>>>>>> serviceAnnotations: ", serviceAnnotations)

	ingress, err := c.ingressLister.Ingresses(namespace).Get(name)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	if ok && errors.IsNotFound(err) {
		// 创建ingress
		ig := c.constructIngress(service)
		_, err := c.client.NetworkingV1().Ingresses(namespace).Create(context.TODO(), ig, metaV1.CreateOptions{})

		if err != nil {
			return err
		}
	} else if !ok && ingress != nil {
		// 删除ingress
		err := c.client.NetworkingV1().Ingresses(namespace).Delete(context.TODO(), name, metaV1.DeleteOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) handlerError(key string, err error) {
	// 如果出现错误，重新加入队列,最大处理10次
	if c.queue.NumRequeues(key) <= maxRetry {
		c.queue.AddRateLimited(key)
		return
	}
	runtime.HandleError(err)
	c.queue.Forget(key)
}

func (c *Controller) constructIngress(service *apiCoreV1.Service) *netV1.Ingress {
	// 构造ingress
	pathType := netV1.PathTypePrefix
	ingress := netV1.Ingress{}
	ingress.ObjectMeta.OwnerReferences = []metaV1.OwnerReference{
		*metaV1.NewControllerRef(service, apiCoreV1.SchemeGroupVersion.WithKind("Service")),
	}
	ingress.Namespace = service.Namespace
	ingress.Name = service.Name
	ingress.Spec = netV1.IngressSpec{
		Rules: []netV1.IngressRule{
			{
				Host: "example.com",
				IngressRuleValue: netV1.IngressRuleValue{
					HTTP: &netV1.HTTPIngressRuleValue{
						Paths: []netV1.HTTPIngressPath{
							{
								Path:     "/",
								PathType: &pathType,
								Backend: netV1.IngressBackend{
									Service: &netV1.IngressServiceBackend{
										Name: service.Name,
										Port: netV1.ServiceBackendPort{
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
