package agentproto

// Node marks are shared by accounting, outbound compilation and local guards.
const NodeMarkMask uint32 = 0xff000000
const NodeMarkPrefix uint32 = 0x43000000
const BootstrapMarkPrefix uint32 = 0x44000000

func BootstrapMark(id int64) (uint32, error) {
	if _, err := NodeMark(id); err != nil {
		return 0, err
	}
	return BootstrapMarkPrefix | uint32(id), nil
}

func NodeMark(id int64) (uint32, error) {
	return (ResourceIdentity{Kind: "node", ID: id}).Mark()
}

const ForwardBootstrapMarkPrefix uint32 = 0x46000000

func ForwardBootstrapMark(id int64) (uint32, error) {
	if _, err := (ResourceIdentity{Kind: "forward", ID: id}).Mark(); err != nil {
		return 0, err
	}
	return ForwardBootstrapMarkPrefix | uint32(id), nil
}
