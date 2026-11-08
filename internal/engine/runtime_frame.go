package engine

// runtimeFrame stores one lexical scope without exposing its binding map.
// Child scopes keep their own bindings and resolve missing names through parent.
type runtimeFrame struct {
	parent   *runtimeFrame
	bindings map[string]RuntimeValue
}

func newRuntimeRootFrame(bindings map[string]RuntimeValue) *runtimeFrame {
	return &runtimeFrame{bindings: cloneRuntimeBindings(bindings)}
}

func (frame *runtimeFrame) child(bindings map[string]RuntimeValue) *runtimeFrame {
	return &runtimeFrame{parent: frame, bindings: cloneRuntimeBindings(bindings)}
}

func (frame *runtimeFrame) childBinding(name string, value RuntimeValue) *runtimeFrame {
	return &runtimeFrame{parent: frame, bindings: map[string]RuntimeValue{name: value}}
}

func (frame *runtimeFrame) lookup(name string) (RuntimeValue, bool) {
	for current := frame; current != nil; current = current.parent {
		if value, ok := current.bindings[name]; ok {
			return value, true
		}
	}

	return RuntimeValue{}, false
}

func (frame *runtimeFrame) flatten() map[string]RuntimeValue {
	var chain []*runtimeFrame
	for current := frame; current != nil; current = current.parent {
		chain = append(chain, current)
	}
	bindings := make(map[string]RuntimeValue)
	for index := len(chain) - 1; index >= 0; index-- {
		for name, value := range chain[index].bindings {
			bindings[name] = value
		}
	}

	return bindings
}

func cloneRuntimeBindings(bindings map[string]RuntimeValue) map[string]RuntimeValue {
	if len(bindings) == 0 {
		return nil
	}

	cloned := make(map[string]RuntimeValue, len(bindings))
	for name, value := range bindings {
		cloned[name] = value
	}

	return cloned
}
