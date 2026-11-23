package config

// Keys splits each segment of path into keys, on every character that isn't
// an ASCII letter, digit or underscore, and returns all the keys in order.
// Empty keys, from repeated, leading or trailing separators, are dropped. Case
// is preserved.
//
// Every provider should use it, so that they all look in the equivalent place
// for the same function.
//
// A function named "http.server" under "svc" has the path ["svc", "http.server"],
// which Keys turns into ["svc", "http", "server"]. A provider then lays those
// keys out its own way: the env provider joins them with underscores
// (SVC_HTTP_SERVER) and the JSON provider nests objects ("svc" > "http" >
// "server").
func Keys(path []string) []string {
	keys := make([]string, 0, len(path))

	for _, segment := range path {
		start := -1

		for i := 0; i < len(segment); i++ {
			if isKeyByte(segment[i]) {
				if start < 0 {
					start = i
				}

				continue
			}

			if start >= 0 {
				keys = append(keys, segment[start:i])
				start = -1
			}
		}

		if start >= 0 {
			keys = append(keys, segment[start:])
		}
	}

	return keys
}

func isKeyByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}
