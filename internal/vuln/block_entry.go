package vuln

import (
	"encoding/json"
	"strconv"
)

// BlockSet is the value of `block` in the audit event and the journal entry of a change that set a download block.
const BlockSet = "set"

// BlockEntry is the journal entry of a change that set a download block (R-HA-054, R-VULN-026, Q-VULN-48, Q-VULN-130). Every
// producer builds it here (a triage, the reopen, the four changes of the evaluation) and the replay reads it back, so that the
// two cannot drift. All members are text: RepositoryID is the decimal text of repositories.id, Kind is `vulnerability` or
// `malicious`, Severity the finding's level.
type BlockEntry struct {
	Finding      string `json:"finding"`
	RepositoryID string `json:"repositoryId"`
	Repository   string `json:"repository"`
	Ecosystem    string `json:"ecosystem"`
	Package      string `json:"package"`
	Version      string `json:"version"`
	Advisory     string `json:"advisory"`
	Kind         string `json:"kind"`
	Severity     string `json:"severity"`
	Block        string `json:"block"`
}

// NewBlockEntry builds the entry of a change that set a block; repositoryID is repositories.id and repository its qualified name.
func NewBlockEntry(finding string, repositoryID int64, repository, ecosystem, pkg, version, advisoryID, kind, severity string) BlockEntry {
	return BlockEntry{
		Finding: finding, RepositoryID: strconv.FormatInt(repositoryID, 10), Repository: repository, Ecosystem: ecosystem, Package: pkg,
		Version: version, Advisory: advisoryID, Kind: kind, Severity: severity, Block: BlockSet,
	}
}

// JSON is the entry as the journal stores it.
func (e BlockEntry) JSON() (json.RawMessage, error) { return json.Marshal(e) }
