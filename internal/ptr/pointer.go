package ptr

// Deref returns pointer's value; empty if nil.
func Deref[T any](p *T) T {
	if p != nil {
		return *p
	}
	var v T
	return v
}
