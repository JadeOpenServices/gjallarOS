package winesandbox

// MaxID is the longest program id.
const MaxID = 48

// ValidID says whether id can name a program: lowercase letters, digits
// and inner dashes, starting with a letter. The id shows in a password
// dialog and in file and socket names, so nothing else is allowed.
func ValidID(id string) bool {
	if id == "" || len(id) > MaxID || id[0] < 'a' || id[0] > 'z' || id[len(id)-1] == '-' {
		return false
	}
	for _, c := range []byte(id) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
