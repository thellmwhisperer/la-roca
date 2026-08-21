package parsers

type parentGraphNode struct {
	id     string
	parent string
}

func survivingParents(nodes []parentGraphNode, byID map[string]int,
	discarded func(int) bool) []int {
	parents := make([]int, len(nodes))
	resolved := make([]int, len(nodes))
	visiting := make([]bool, len(nodes))
	for index := range resolved {
		resolved[index] = -2
	}
	var resolve func(int) int
	resolve = func(index int) int {
		if !discarded(index) {
			return index
		}
		if resolved[index] != -2 {
			return resolved[index]
		}
		if visiting[index] {
			return -1
		}
		visiting[index] = true
		parent := -1
		if position, found := byID[nodes[index].parent]; found {
			parent = resolve(position - 1)
		}
		visiting[index] = false
		resolved[index] = parent
		return parent
	}
	for index := range parents {
		parents[index] = -1
		if position, found := byID[nodes[index].parent]; found {
			parents[index] = resolve(position - 1)
		}
	}
	return parents
}

func discardParentCycles(nodes []parentGraphNode, parents []int,
	discarded func(int) bool, mark func(int)) {
	state := make([]uint8, len(nodes))
	var visit func(int)
	visit = func(index int) {
		state[index] = 1
		parent := parents[index]
		if parent >= 0 && !discarded(parent) {
			switch state[parent] {
			case 0:
				visit(parent)
			case 1:
				for member := parent; ; member = parents[member] {
					mark(member)
					if member == index {
						break
					}
				}
			}
		}
		state[index] = 2
	}
	for index := range nodes {
		if !discarded(index) && state[index] == 0 {
			visit(index)
		}
	}
}
