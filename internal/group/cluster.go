package group

// unionFind is a tiny disjoint-set for identity clustering inside one
// candidate group.
type unionFind struct {
	parent []int
}

func newUnionFind(n int) *unionFind {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return &unionFind{parent: p}
}

func (u *unionFind) find(x int) int {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]]
		x = u.parent[x]
	}
	return x
}

func (u *unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[rb] = ra
	}
}

// groups collects the component of every member, ordering the components
// by their first member so group ids are stable across runs.
func (u *unionFind) groups(imgs []Image) [][]Image {
	byRoot := map[int][]Image{}
	order := make([]int, 0, len(imgs))
	for i, im := range imgs {
		r := u.find(i)
		if _, ok := byRoot[r]; !ok {
			order = append(order, r)
		}
		byRoot[r] = append(byRoot[r], im)
	}
	out := make([][]Image, 0, len(order))
	for _, r := range order {
		out = append(out, byRoot[r])
	}
	return out
}
