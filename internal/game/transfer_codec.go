package game

import "encoding/json"

// MarshalTransferPayload encodes a TransferPayload to JSON bytes.
func MarshalTransferPayload(p *TransferPayload) ([]byte, error) {
	return json.Marshal(p)
}

// UnmarshalTransferPayload decodes a TransferPayload from JSON bytes.
func UnmarshalTransferPayload(data []byte) (*TransferPayload, error) {
	var p TransferPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// MarshalReplicaSnapshot encodes a ReplicaSnapshot to JSON bytes.
func MarshalReplicaSnapshot(s *ReplicaSnapshot) ([]byte, error) {
	return json.Marshal(s)
}

// UnmarshalReplicaSnapshot decodes a ReplicaSnapshot from JSON bytes.
func UnmarshalReplicaSnapshot(data []byte) (*ReplicaSnapshot, error) {
	var s ReplicaSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
