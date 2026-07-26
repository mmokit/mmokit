package universe

import (
	"strconv"
	"strings"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

// Replication receipts reuse existing MeshFrame fields so this internal
// transport handshake does not change the protobuf schema. Cell IDs cannot
// begin with this prefix (canonical IDs begin with "cell_"), making the
// DestCellId namespace unambiguous at the mesh boundary.
const (
	replicationReceiptNamespacePrefix = "@mmokit/repl-receipt/"
	replicationReceiptMarkerPrefix    = replicationReceiptNamespacePrefix + "v2/"
	replicationReceiptChannel         = ^uint32(0)
	replicationReceiptPayloadV1       = byte(1)
)

// replicationReceiptMarker binds an opaque token to the host that originated
// the tracked frame. Epoch alone is not enough during handoff preparation:
// the destination host can temporarily hold the same session epoch before the
// gateway switches authority. Encoding the source host lets the gateway reject
// that premature frame and return the receipt to the actual sender.
func replicationReceiptMarker(sourceHostID string, token uint64) string {
	return replicationReceiptMarkerPrefix + strconv.Itoa(len(sourceHostID)) + "/" + sourceHostID + "/" + strconv.FormatUint(token, 10)
}

func hasReplicationReceiptMarker(marker string) bool {
	return strings.HasPrefix(marker, replicationReceiptNamespacePrefix)
}

func parseReplicationReceiptMarker(marker string) (string, uint64, bool) {
	if !strings.HasPrefix(marker, replicationReceiptMarkerPrefix) {
		return "", 0, false
	}
	raw := strings.TrimPrefix(marker, replicationReceiptMarkerPrefix)
	lengthEnd := strings.IndexByte(raw, '/')
	if lengthEnd <= 0 {
		return "", 0, false
	}
	hostLen64, err := strconv.ParseUint(raw[:lengthEnd], 10, 31)
	if err != nil || hostLen64 == 0 {
		return "", 0, false
	}
	hostStart := lengthEnd + 1
	if hostStart >= len(raw) || hostLen64 >= uint64(len(raw)-hostStart) {
		return "", 0, false
	}
	hostEnd := hostStart + int(hostLen64)
	if hostEnd >= len(raw) || raw[hostEnd] != '/' {
		return "", 0, false
	}
	tokenRaw := raw[hostEnd+1:]
	token, err := strconv.ParseUint(tokenRaw, 10, 64)
	if err != nil || token == 0 {
		return "", 0, false
	}
	return raw[hostStart:hostEnd], token, true
}

func encodeReplicationReceiptResult(result pkgnet.SendResult) []byte {
	return []byte{replicationReceiptPayloadV1, byte(result.Disposition), byte(result.Delivery)}
}

func decodeReplicationReceiptResult(data []byte) (pkgnet.SendResult, bool) {
	if len(data) != 3 || data[0] != replicationReceiptPayloadV1 {
		return pkgnet.SendResult{}, false
	}
	disposition := pkgnet.SendDisposition(data[1])
	delivery := pkgnet.DeliveryClass(data[2])
	if disposition > pkgnet.SendIndeterminate || delivery > pkgnet.DeliveryReliableOrdered {
		return pkgnet.SendResult{}, false
	}
	return pkgnet.SendResult{Disposition: disposition, Delivery: delivery}, true
}

func newReplicationReceiptFrame(sourceHostID, gatewayID string, connID uint32, epoch uint64, token uint64, result pkgnet.SendResult) *meshpb.MeshFrame {
	return &meshpb.MeshFrame{
		DestCellId: replicationReceiptMarker(sourceHostID, token),
		Msg: &meshpb.MeshFrame_ClientInput{
			ClientInput: &meshpb.ClientInput{
				GatewayId: gatewayID,
				ConnId:    connID,
				Epoch:     epoch,
				Channel:   replicationReceiptChannel,
				Data:      encodeReplicationReceiptResult(result),
			},
		},
	}
}

func decodeReplicationReceiptFrame(frame *meshpb.MeshFrame) (string, uint64, pkgnet.SendResult, bool) {
	if frame == nil {
		return "", 0, pkgnet.SendResult{}, false
	}
	ci := frame.GetClientInput()
	if ci == nil || ci.Channel != replicationReceiptChannel {
		return "", 0, pkgnet.SendResult{}, false
	}
	sourceHostID, token, ok := parseReplicationReceiptMarker(frame.GetDestCellId())
	if !ok {
		return "", 0, pkgnet.SendResult{}, false
	}
	result, ok := decodeReplicationReceiptResult(ci.Data)
	if !ok {
		return "", 0, pkgnet.SendResult{}, false
	}
	return sourceHostID, token, result, true
}
