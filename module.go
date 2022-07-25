package celery

import (
	"errors"
	"sync"

	"github.com/dop251/goja"
	"go.k6.io/k6/js/common"
	"go.k6.io/k6/js/modules"
)

type (
	RootModule struct {
		client *Client
	}

	ModuleInstance struct {
		*RootModule
		vu      modules.VU
		exports map[string]interface{}
	}
)

var (
	_ modules.Module   = &RootModule{}
	_ modules.Instance = &ModuleInstance{}
)

func (root *RootModule) NewModuleInstance(vu modules.VU) modules.Instance {
	mi := &ModuleInstance{
		RootModule: root,
		vu:         vu,
		exports:    make(map[string]interface{}),
	}

	mi.exports["connect"] = mi.Connect
	return mi
}

func (mi *ModuleInstance) Exports() modules.Exports {
	return modules.Exports{
		Named: mi.exports,
	}
}

func init() {
	root := &RootModule{}
	modules.Register("k6/x/celery", root)
}

var initClientOnce = sync.Once{}

func (moduleInstance *ModuleInstance) Connect(brokerUrl string, queueName string) *goja.Object {
	vu := moduleInstance.vu

	rt := vu.Runtime()
	state := vu.State()
	if state != nil {
		common.Throw(rt, errors.New("celery client should be instantiated in init context"))
	}

	initClientOnce.Do(func() {
		newClient, err := newClient(brokerUrl, queueName)
		if err != nil {
			common.Throw(rt, err)
		}

		moduleInstance.client = newClient
	})

	metrics, err := registerMetrics(vu)
	if err != nil {
		common.Throw(rt, err)
	}

	return rt.ToValue(&vuClientWrapper{
		vu:      vu,
		metrics: metrics,
		client:  moduleInstance.client,
	}).ToObject(rt)
}

type vuClientWrapper struct {
	client *Client

	vu      modules.VU
	metrics *celeryMetrics
}

func (wrapper *vuClientWrapper) RunTask(params *TaskRunArgs) {
	vu := wrapper.vu
	rt := vu.Runtime()
	state := vu.State()
	if state == nil {
		common.Throw(rt, errors.New("celery task can't be run in init context"))
	}

	err := wrapper.client.RunTask(params, vu, wrapper.metrics)
	if err != nil {
		common.Throw(rt, err)
	}
}
