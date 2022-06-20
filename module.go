package celery

import (
	"sync"

	"github.com/dop251/goja"
	"go.k6.io/k6/js/modules"
)

type (
	RootModule struct {
		client *Client
	}

	// ModuleInstance represents an instance of the GRPC module for every VU.
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

	mi.exports["Client"] = mi.NewCeleryClient
	return mi
}

func (mi *ModuleInstance) Exports() modules.Exports {
	return modules.Exports{
		Named: mi.exports,
	}
}

var initClientOnce = sync.Once{}

func (mi *ModuleInstance) NewCeleryClient(call goja.ConstructorCall) *goja.Object {
	rt := mi.vu.Runtime()

	initClientOnce.Do(func() {
		brokerUrl := call.Arguments[0].String()
		newClient, err := NewClient(brokerUrl)
		if err != nil {
			panic(err)
		}

		mi.RootModule.client = newClient
	})

	return rt.ToValue(mi.RootModule.client).ToObject(rt)
}

func init() {
	root := &RootModule{}
	modules.Register("k6/x/celery", root)
}
