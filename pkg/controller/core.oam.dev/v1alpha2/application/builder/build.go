package builder

import (
	"encoding/json"
	"fmt"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/build"
	cueparser "cuelang.org/go/cue/parser"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha2"
	"github.com/oam-dev/kubevela/pkg/controller/core.oam.dev/v1alpha2/application/parser"
	"github.com/oam-dev/kubevela/pkg/dsl/process"
)

type builder struct {
	app *parser.Appfile
}

const (
	// OamApplicationLabel is application's metadata label
	OamApplicationLabel = "application.oam.dev"
)

// Build template to applicationConfig & Component
func Build(ns string, app *parser.Appfile) (*v1alpha2.ApplicationConfiguration, []*v1alpha2.Component, error) {
	b := &builder{app}
	return b.CompleteWithContext(ns)
}

// Complete: builder complete rendering
func (b *builder) Complete(ns string) (*v1alpha2.ApplicationConfiguration, []*v1alpha2.Component, error) {
	appconfig := &v1alpha2.ApplicationConfiguration{}
	appconfig.SetGroupVersionKind(v1alpha2.ApplicationConfigurationGroupVersionKind)
	appconfig.Name = b.app.Name()
	appconfig.Namespace = ns
	appconfig.Spec.Components = []v1alpha2.ApplicationConfigurationComponent{}

	if appconfig.Labels == nil {
		appconfig.Labels = map[string]string{}
	}
	appconfig.Labels[OamApplicationLabel] = b.app.Name()

	componets := []*v1alpha2.Component{}
	for _, wl := range b.app.Services() {
		compCtx := map[string]string{"name": wl.Name()}

		component, err := wl.Eval(newLoader(compCtx))
		if err != nil {
			return nil, nil, err
		}

		component.Namespace = ns
		component.Name = wl.Name()
		if component.Labels == nil {
			component.Labels = map[string]string{}
		}
		component.Labels[OamApplicationLabel] = b.app.Name()
		component.SetGroupVersionKind(v1alpha2.ComponentGroupVersionKind)
		componets = append(componets, component)

		comp := v1alpha2.ApplicationConfigurationComponent{
			ComponentName: wl.Name(),
			Traits:        []v1alpha2.ComponentTrait{},
		}

		for _, trait := range wl.Traits() {
			ctraits, err := trait.Eval(newLoader(compCtx))
			if err != nil {
				return nil, nil, err
			}
			comp.Traits = append(comp.Traits, ctraits...)
		}
		appconfig.Spec.Components = append(appconfig.Spec.Components, comp)
	}
	return appconfig, componets, nil
}

func (b *builder) CompleteWithContext(ns string) (*v1alpha2.ApplicationConfiguration, []*v1alpha2.Component, error) {
	appconfig := &v1alpha2.ApplicationConfiguration{}
	appconfig.SetGroupVersionKind(v1alpha2.ApplicationConfigurationGroupVersionKind)
	appconfig.Name = b.app.Name()
	appconfig.Namespace = ns
	appconfig.Spec.Components = []v1alpha2.ApplicationConfigurationComponent{}

	if appconfig.Labels == nil {
		appconfig.Labels = map[string]string{}
	}
	appconfig.Labels[OamApplicationLabel] = b.app.Name()

	componets := []*v1alpha2.Component{}
	for _, wl := range b.app.Services() {
		pCtx := process.NewContext(wl.Name())
		if err := wl.EvalContext(pCtx); err != nil {
			return nil, nil, err
		}
		for _, tr := range wl.Traits() {
			if err := tr.EvalContext(pCtx); err != nil {
				return nil, nil, err
			}
		}
		comp, acComp, err := generateOAM(pCtx)
		if err != nil {
			return nil, nil, err
		}
		comp.Name = wl.Name()
		acComp.ComponentName = comp.Name

		comp.Namespace = ns
		if comp.Labels == nil {
			comp.Labels = map[string]string{}
		}
		comp.Labels[OamApplicationLabel] = b.app.Name()
		comp.SetGroupVersionKind(v1alpha2.ComponentGroupVersionKind)

		componets = append(componets, comp)
		appconfig.Spec.Components = append(appconfig.Spec.Components, *acComp)
	}

	return appconfig, componets, nil
}

func generateOAM(pCtx process.Context) (*v1alpha2.Component, *v1alpha2.ApplicationConfigurationComponent, error) {
	base, assists := pCtx.Output()
	componetWorkload, err := base.Object(nil)
	if err != nil {
		return nil, nil, err
	}
	component := &v1alpha2.Component{}
	component.Spec.Workload.Object = componetWorkload

	acComponent := &v1alpha2.ApplicationConfigurationComponent{}
	acComponent.Traits = []v1alpha2.ComponentTrait{}
	for _, assist := range assists {
		traitRef, err := assist.Object(nil)
		if err != nil {
			return nil, nil, err
		}
		acComponent.Traits = append(acComponent.Traits, v1alpha2.ComponentTrait{
			Trait: runtime.RawExtension{
				Object: traitRef,
			},
		})
	}
	return component, acComponent, nil
}

type loader struct {
	files map[string]*ast.File
	err   error
}

func newLoader(ctx interface{}) parser.Render {
	l := &loader{
		files: map[string]*ast.File{},
	}
	const key = "context"
	f, err := cueparser.ParseFile(key, marshal(key, ctx))
	if err != nil {
		l.err = errors.Errorf("loader parse %s error", key)
	}
	l.files[key] = f
	return l
}

// WithTemplate: loader add template
func (l *loader) WithTemplate(raw string) parser.Render {
	if l.err != nil {
		return l
	}
	f, err := cueparser.ParseFile("-", raw)
	if err != nil {
		l.err = errors.Errorf("loader parse template error")
	}
	l.files["-"] = f
	return l
}

// WithContext: loader add context
func (l *loader) WithContext(ctx interface{}) parser.Render {
	if l.err != nil {
		return l
	}
	const key = "context"
	f, err := cueparser.ParseFile(key, marshal(key, ctx))
	if err != nil {
		l.err = errors.Errorf("loader parse %s error", key)
	}
	l.files[key] = f
	return l
}

// WithParams: loader add params
func (l *loader) WithParams(params interface{}) parser.Render {
	if l.err != nil {
		return l
	}
	const key = "parameter"
	f, err := cueparser.ParseFile(key, marshal(key, params))
	if err != nil {
		l.err = errors.Errorf("loader parse %s error", key)
	}
	l.files[key] = f
	return l
}

// Complete: loader generate cue instance
func (l *loader) Complete() (*cue.Instance, error) {
	if l.err != nil {
		return nil, l.err
	}
	bi := build.NewContext().NewInstance("", nil)
	for fname, f := range l.files {
		if err := bi.AddSyntax(f); err != nil {
			return nil, errors.WithMessagef(err, "loader AddSyntax %s", fname)
		}
	}
	insts := cue.Build([]*build.Instance{bi})

	var ret *cue.Instance
	for _, inst := range insts {
		if err := inst.Value().Validate(cue.Concrete(true)); err != nil {
			return nil, errors.WithMessagef(err, "loader cue-instance validate")
		}
		ret = inst
	}
	return ret, nil
}

func marshal(key string, v interface{}) string {
	_body, _ := json.Marshal(v)
	return fmt.Sprintf("%s: %s", key, string(_body))
}
