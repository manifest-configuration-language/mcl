package engine

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// runtimeBundleIndexはArtifact順DFSの値列を順序統計treapで保持し、部分木の置換範囲を局所更新します。
type runtimeBundleIndex struct {
	bundle       *RuntimeBundle
	root         *runtimeOrderNode
	roots        []*runtimeOrderNode
	values       map[*RuntimeValue]map[*runtimeOrderNode]struct{}
	nodes        map[*NodeIdentity]map[*runtimeOrderNode]struct{}
	nextPriority uint64
	valueCount   int
}

type runtimeOrderNode struct {
	value       *RuntimeValue
	kind        Kind
	identity    *NodeIdentity
	priority    uint64
	size        int
	subtreeSize int
	children    []*runtimeOrderNode
	parentValue *runtimeOrderNode
	edge        string
	artifact    int
	left, right *runtimeOrderNode
	parent      *runtimeOrderNode
}

func newRuntimeBundleIndex(bundle *RuntimeBundle) *runtimeBundleIndex {
	index := &runtimeBundleIndex{
		bundle: bundle,
		roots:  make([]*runtimeOrderNode, len(bundle.Artifacts)),
		values: make(map[*RuntimeValue]map[*runtimeOrderNode]struct{}),
		nodes:  make(map[*NodeIdentity]map[*runtimeOrderNode]struct{}),
	}
	for artifactIndex := range bundle.Artifacts {
		value := &bundle.Artifacts[artifactIndex].Value
		index.roots[artifactIndex] = index.buildSubtree(value, nil, "", artifactIndex+1)
	}

	return index
}

func (index *runtimeBundleIndex) buildSubtree(value *RuntimeValue, parent *runtimeOrderNode, edge string, artifactIndex int) *runtimeOrderNode {
	node := &runtimeOrderNode{value: value, kind: value.Kind(), priority: index.nextPriorityValue(), size: 1}
	node.parentValue, node.edge, node.artifact = parent, edge, artifactIndex
	if node.kind == Node {
		node.identity = value.NodeIdentity()
	}
	index.root = mergeRuntimeOrderNodes(index.root, node)
	index.valueCount++
	occurrences := index.values[value]
	if occurrences == nil {
		occurrences = make(map[*runtimeOrderNode]struct{})
		index.values[value] = occurrences
	}
	occurrences[node] = struct{}{}
	node.subtreeSize = 1
	if value.Kind() == Node {
		if identity := value.NodeIdentity(); identity != nil {
			occurrences := index.nodes[identity]
			if occurrences == nil {
				occurrences = make(map[*runtimeOrderNode]struct{})
				index.nodes[identity] = occurrences
			}
			occurrences[node] = struct{}{}
		}
		if value.Base != nil {
			child := index.buildSubtree(value.Base, node, "", artifactIndex)
			node.children = append(node.children, child)
			node.subtreeSize += child.subtreeSize
		}
	} else {
		//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
		switch value.Kind() {
		case List, AdapterBundle:
			for childIndex := range value.List {
				child := &value.List[childIndex]
				childNode := index.buildSubtree(child, node, "item "+strconv.Itoa(childIndex), artifactIndex)
				node.children = append(node.children, childNode)
				node.subtreeSize += childNode.subtreeSize
			}
		case Map:
			for childIndex := range value.Map {
				child := &value.Map[childIndex].Value
				childNode := index.buildSubtree(child, node, fmt.Sprintf("field %q", value.Map[childIndex].Key), artifactIndex)
				node.children = append(node.children, childNode)
				node.subtreeSize += childNode.subtreeSize
			}
		}
	}

	return node
}

func (index *runtimeBundleIndex) nextPriorityValue() uint64 {
	index.nextPriority++
	value := index.nextPriority + 0x9e3779b97f4a7c15
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb

	return value ^ (value >> 31)
}

func (index *runtimeBundleIndex) replace(valueNode *runtimeOrderNode, state *EvaluationState) error {
	occurrences := make([]*runtimeOrderNode, 0, len(index.values[valueNode.value]))
	for occurrence := range index.values[valueNode.value] {
		occurrences = append(occurrences, occurrence)
	}
	slices.SortFunc(occurrences, func(left, right *runtimeOrderNode) int {
		return runtimeOrderRank(right) - runtimeOrderRank(left)
	})
	touched := make(map[*NodeIdentity]struct{})
	inserted := make([]*runtimeOrderNode, 0, len(occurrences))
	for _, occurrence := range occurrences {
		inserted = append(inserted, index.replaceOccurrence(occurrence, touched))
	}
	for identity := range touched {
		identityOccurrences := index.nodes[identity]
		if len(identityOccurrences) == 0 {
			delete(state.NodeIndex, identity)

			continue
		}
		for node := range identityOccurrences {
			state.NodeIndex[identity] = runtimeNodeRecord{value: node.value, ancestors: index.ancestors(node)}
		}
	}

	return index.checkAndChargeRebuild(state, inserted, touched)
}

func (index *runtimeBundleIndex) replaceOccurrence(valueNode *runtimeOrderNode, touched map[*NodeIdentity]struct{}) *runtimeOrderNode {
	start := runtimeOrderRank(valueNode)
	oldCount := valueNode.subtreeSize
	left, rest := splitRuntimeOrderNodes(index.root, start-1)
	removed, right := splitRuntimeOrderNodes(rest, oldCount)
	index.root = mergeRuntimeOrderNodes(left, right)
	index.valueCount -= oldCount
	forEachRuntimeOrderNode(removed, func(node *runtimeOrderNode) {
		delete(index.values[node.value], node)
		if len(index.values[node.value]) == 0 {
			delete(index.values, node.value)
		}
		if node.kind == Node {
			if identity := node.identity; identity != nil {
				touched[identity] = struct{}{}
				delete(index.nodes[identity], node)
				if len(index.nodes[identity]) == 0 {
					delete(index.nodes, identity)
				}
			}
		}
	})
	inserted := index.buildDetachedSubtree(valueNode.value, valueNode.parentValue, valueNode.edge, valueNode.artifact, touched, valueNode)
	insertStart := start
	left, right = splitRuntimeOrderNodes(index.root, insertStart-1)
	index.root = mergeRuntimeOrderNodes(mergeRuntimeOrderNodes(left, inserted), right)

	return inserted
}

func (index *runtimeBundleIndex) buildDetachedSubtree(value *RuntimeValue, parent *runtimeOrderNode, edge string, artifactIndex int, touched map[*NodeIdentity]struct{}, reusable *runtimeOrderNode) *runtimeOrderNode {
	node := reusable
	if node == nil {
		node = &runtimeOrderNode{priority: index.nextPriorityValue()}
	}
	node.value, node.kind, node.size = value, value.Kind(), 1
	node.parentValue, node.edge, node.artifact = parent, edge, artifactIndex
	node.left, node.right, node.parent = nil, nil, nil
	node.children = nil
	node.identity = nil
	if node.kind == Node {
		node.identity = value.NodeIdentity()
	}
	index.valueCount++
	occurrences := index.values[value]
	if occurrences == nil {
		occurrences = make(map[*runtimeOrderNode]struct{})
		index.values[value] = occurrences
	}
	occurrences[node] = struct{}{}
	count := 1
	subtree := node
	if node.kind == Node {
		if identity := node.identity; identity != nil {
			touched[identity] = struct{}{}
			occurrences := index.nodes[identity]
			if occurrences == nil {
				occurrences = make(map[*runtimeOrderNode]struct{})
				index.nodes[identity] = occurrences
			}
			occurrences[node] = struct{}{}
		}
		if value.Base != nil {
			child := index.buildDetachedSubtree(value.Base, node, "", artifactIndex, touched, nil)
			node.children = append(node.children, child)
			subtree = mergeRuntimeOrderNodes(subtree, child)
			count += runtimeOrderSize(child)
		}
	} else {
		//nolint:exhaustive // 部分的なKind判定であり、その他の値は後続の共通処理で扱います。
		switch value.Kind() {
		case List, AdapterBundle:
			for childIndex := range value.List {
				child := index.buildDetachedSubtree(&value.List[childIndex], node, "item "+strconv.Itoa(childIndex), artifactIndex, touched, nil)
				node.children = append(node.children, child)
				subtree = mergeRuntimeOrderNodes(subtree, child)
				count += runtimeOrderSize(child)
			}
		case Map:
			for childIndex := range value.Map {
				child := index.buildDetachedSubtree(&value.Map[childIndex].Value, node, fmt.Sprintf("field %q", value.Map[childIndex].Key), artifactIndex, touched, nil)
				node.children = append(node.children, child)
				subtree = mergeRuntimeOrderNodes(subtree, child)
				count += runtimeOrderSize(child)
			}
		}
	}
	if subtree == node {
		node.size = count
	} else {
		subtree.size = count
	}
	node.subtreeSize = count

	return subtree
}

func (index *runtimeBundleIndex) checkAndChargeRebuild(state *EvaluationState, inserted []*runtimeOrderNode, touched map[*NodeIdentity]struct{}) error {
	invalidRank, duplicateRank := 0, 0
	for _, root := range inserted {
		forEachRuntimeOrderNode(root, func(node *runtimeOrderNode) {
			if node.value.Kind() == Node && (node.value.NodeIdentity() == nil || node.value.Base == nil) {
				rank := runtimeOrderRank(node)
				if invalidRank == 0 || rank < invalidRank {
					invalidRank = rank
				}
			}
		})
	}
	for identity := range touched {
		first, second := 0, 0
		for node := range index.nodes[identity] {
			rank := runtimeOrderRank(node)
			if first == 0 || rank < first {
				second, first = first, rank
			} else if second == 0 || rank < second {
				second = rank
			}
		}
		if second != 0 && (duplicateRank == 0 || second < duplicateRank) {
			duplicateRank = second
		}
	}
	structuralRank := invalidRank
	if duplicateRank != 0 && (structuralRank == 0 || duplicateRank < structuralRank) {
		structuralRank = duplicateRank
	}
	budgetRank := 0
	if state.Budget != nil && state.Budget.steps+index.valueCount > maxEvaluationSteps {
		budgetRank = maxEvaluationSteps - state.Budget.steps + 1
	}
	if index.valueCount > maxBundleValueNodes {
		if budgetRank != 0 && budgetRank <= maxBundleValueNodes && (structuralRank == 0 || budgetRank <= structuralRank) {
			return index.budgetError(state, budgetRank)
		}
		if structuralRank != 0 && structuralRank <= maxBundleValueNodes && (budgetRank == 0 || structuralRank < budgetRank) {
			if state.Budget != nil {
				state.Budget.steps += structuralRank
			}
			if invalidRank != 0 && invalidRank <= duplicateRank {
				return index.wrapOccurrenceError(runtimeOrderAt(index.root, invalidRank), errors.New("invalid Node value"))
			}

			return index.wrapOccurrenceError(runtimeOrderAt(index.root, duplicateRank), errors.New("the same Node appears more than once in the final output"))
		}
		if state.Budget != nil {
			state.Budget.steps += maxBundleValueNodes
		}
		value := runtimeOrderAt(index.root, maxBundleValueNodes+1).value
		path, span := value.Source()

		return index.wrapOccurrenceError(runtimeOrderAt(index.root, maxBundleValueNodes+1), sourceDiagnostic(path, span, fmt.Errorf("final Bundle value node limit exceeds %d", maxBundleValueNodes)))
	}
	if state.Budget == nil {
		if invalidRank != 0 && (duplicateRank == 0 || invalidRank <= duplicateRank) {
			return index.wrapOccurrenceError(runtimeOrderAt(index.root, invalidRank), errors.New("invalid Node value"))
		}
		if duplicateRank != 0 {
			return index.wrapOccurrenceError(runtimeOrderAt(index.root, duplicateRank), errors.New("the same Node appears more than once in the final output"))
		}

		return nil
	}
	if budgetRank != 0 && (structuralRank == 0 || budgetRank <= structuralRank) {
		return index.budgetError(state, budgetRank)
	}
	if invalidRank != 0 && (duplicateRank == 0 || invalidRank <= duplicateRank) {
		state.Budget.steps += invalidRank

		return index.wrapOccurrenceError(runtimeOrderAt(index.root, invalidRank), errors.New("invalid Node value"))
	}
	if duplicateRank != 0 {
		state.Budget.steps += duplicateRank

		return index.wrapOccurrenceError(runtimeOrderAt(index.root, duplicateRank), errors.New("the same Node appears more than once in the final output"))
	}
	if budgetRank != 0 {
		return index.budgetError(state, budgetRank)
	}
	state.Budget.steps += index.valueCount

	return nil
}

func (index *runtimeBundleIndex) budgetError(state *EvaluationState, rank int) error {
	state.Budget.steps = maxEvaluationSteps + 1
	value := runtimeOrderAt(index.root, rank).value
	path, span := value.Source()

	return index.wrapOccurrenceError(runtimeOrderAt(index.root, rank), sourceDiagnostic(path, span, fmt.Errorf("evaluation step limit exceeds %d", maxEvaluationSteps)))
}

func (index *runtimeBundleIndex) ancestors(node *runtimeOrderNode) []*NodeIdentity {
	ancestors := make([]*NodeIdentity, 0)
	for current := node; current != nil; current = current.parentValue {
		if current.kind == Node && current.identity != nil {
			ancestors = append(ancestors, current.identity)
		}
	}
	for left, right := 0, len(ancestors)-1; left < right; left, right = left+1, right-1 {
		ancestors[left], ancestors[right] = ancestors[right], ancestors[left]
	}

	return ancestors
}

func (index *runtimeBundleIndex) wrapOccurrenceError(node *runtimeOrderNode, err error) error {
	for current := node; current != nil; current = current.parentValue {
		if current.edge != "" {
			err = fmt.Errorf("%s: %w", current.edge, err)
		}
		if current.parentValue == nil {
			if current.artifact != 0 {
				err = fmt.Errorf("emitted value %d: %w", current.artifact, err)
			}

			break
		}
	}

	return err
}

func mergeRuntimeOrderNodes(left, right *runtimeOrderNode) *runtimeOrderNode {
	if left == nil {
		if right != nil {
			right.parent = nil
		}

		return right
	}
	if right == nil {
		left.parent = nil

		return left
	}
	if left.priority < right.priority {
		left.right = mergeRuntimeOrderNodes(left.right, right)
		left.right.parent = left
		updateRuntimeOrderSize(left)
		left.parent = nil

		return left
	}
	right.left = mergeRuntimeOrderNodes(left, right.left)
	right.left.parent = right
	updateRuntimeOrderSize(right)
	right.parent = nil

	return right
}

func splitRuntimeOrderNodes(root *runtimeOrderNode, count int) (*runtimeOrderNode, *runtimeOrderNode) {
	if root == nil {
		return nil, nil
	}
	leftSize := runtimeOrderSize(root.left)
	if count <= leftSize {
		left, right := splitRuntimeOrderNodes(root.left, count)
		root.left = right
		if right != nil {
			right.parent = root
		}
		updateRuntimeOrderSize(root)
		root.parent = nil
		if left != nil {
			left.parent = nil
		}

		return left, root
	}
	left, right := splitRuntimeOrderNodes(root.right, count-leftSize-1)
	root.right = left
	if left != nil {
		left.parent = root
	}
	updateRuntimeOrderSize(root)
	root.parent = nil
	if right != nil {
		right.parent = nil
	}

	return root, right
}

func runtimeOrderRank(node *runtimeOrderNode) int {
	rank := runtimeOrderSize(node.left) + 1
	for node.parent != nil {
		if node == node.parent.right {
			rank += runtimeOrderSize(node.parent.left) + 1
		}
		node = node.parent
	}

	return rank
}

func runtimeOrderAt(root *runtimeOrderNode, rank int) *runtimeOrderNode {
	for root != nil {
		leftSize := runtimeOrderSize(root.left)
		switch {
		case rank == leftSize+1:
			return root
		case rank <= leftSize:
			root = root.left
		default:
			rank -= leftSize + 1
			root = root.right
		}
	}

	return nil
}

func forEachRuntimeOrderNode(root *runtimeOrderNode, visit func(*runtimeOrderNode)) {
	if root == nil {
		return
	}
	forEachRuntimeOrderNode(root.left, visit)
	visit(root)
	forEachRuntimeOrderNode(root.right, visit)
}

func runtimeOrderSize(node *runtimeOrderNode) int {
	if node == nil {
		return 0
	}

	return node.size
}

func updateRuntimeOrderSize(node *runtimeOrderNode) {
	node.size = runtimeOrderSize(node.left) + runtimeOrderSize(node.right) + 1
}
