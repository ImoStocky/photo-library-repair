package group

// Pipeline is the Chain: Aggregate (candidates) → IdentityStrategy →
// assignment map for the store. ResolveGroup / plan stays a separate phase.
type Pipeline struct {
	CandidateRadius int
	Identity        IdentityStrategy
}

// Run partitions imgs into identity groups and returns imageID → groupID
// (1..n in order of first appearance) plus the member lists.
func (p Pipeline) Run(imgs []Image) (map[string]int64, [][]Image, error) {
	identity := p.Identity
	if identity == nil {
		identity = PassThrough{}
	}
	_, candidates := Aggregate(imgs, p.CandidateRadius)
	var identities [][]Image
	for _, c := range candidates {
		parts, err := identity.Refine(c)
		if err != nil {
			return nil, nil, err
		}
		identities = append(identities, parts...)
	}
	assign := map[string]int64{}
	var gid int64
	ordered := make([][]Image, 0, len(identities))
	for _, members := range identities {
		if len(members) == 0 {
			continue
		}
		gid++
		for _, im := range members {
			assign[im.ID] = gid
		}
		ordered = append(ordered, members)
	}
	return assign, ordered, nil
}
