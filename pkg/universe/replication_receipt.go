package universe

import (
	meshpb "github.com/mmokit/mmokit/gen/go/meshpb"
	pkgnet "github.com/mmokit/mmokit/pkg/net"
)

// Replication receipts are the gateway's answer to a tracked ClientFrame,
// telling the sending host what delivery class the frame actually achieved.
//
// They used to ride the ClientInput arm on a reserved ^uint32(0) channel with
// their identity packed into DestCellId as "@mmokit/repl-receipt/v2/<len>/
// <host>/<token>" — a cell-ID string field carrying neither a cell nor an ID.
// That workaround existed only because a proto change felt expensive, and it
// cost a namespace prefix, four parse helpers, and an arm of routeInboundFrame
// that had to intercept control traffic before game input decoding could see
// it. Both legs are now typed fields.

// trackedClientFrame builds the outbound leg: an ordinary ClientFrame that
// additionally asks the receiving gateway for a receipt.
//
// sourceHostID binds the token to the host that originated the frame. Epoch
// alone is not enough during handoff preparation — the destination host can
// briefly hold the same session epoch before the gateway switches authority —
// so carrying the source lets the gateway reject that premature frame and
// return the receipt to whoever actually sent it.
func trackedClientFrame(sourceHostID, gatewayID string, connID uint32, epoch, token uint64, data []byte) *meshpb.MeshFrame {
	return &meshpb.MeshFrame{
		Msg: &meshpb.MeshFrame_ClientFrame{
			ClientFrame: &meshpb.ClientFrame{
				GatewayId:    gatewayID,
				ConnId:       connID,
				Data:         data,
				Epoch:        epoch,
				SourceHostId: sourceHostID,
				ReceiptToken: token,
			},
		},
	}
}

// newReplicationReceiptFrame builds the return leg.
func newReplicationReceiptFrame(sourceHostID, gatewayID string, connID uint32, epoch, token uint64, result pkgnet.SendResult) *meshpb.MeshFrame {
	return &meshpb.MeshFrame{
		Msg: &meshpb.MeshFrame_ReplicationReceipt{
			ReplicationReceipt: &meshpb.ReplicationReceipt{
				SourceHostId: sourceHostID,
				GatewayId:    gatewayID,
				ConnId:       connID,
				Epoch:        epoch,
				ReceiptToken: token,
				Disposition:  uint32(result.Disposition),
				Delivery:     uint32(result.Delivery),
			},
		},
	}
}

// receiptResult validates the wire enums and rebuilds the SendResult. Both
// fields are peer-supplied, so an out-of-range value is a rejection rather
// than something to be cast blindly into a typed enum.
func receiptResult(rr *meshpb.ReplicationReceipt) (pkgnet.SendResult, bool) {
	disposition := pkgnet.SendDisposition(rr.GetDisposition())
	delivery := pkgnet.DeliveryClass(rr.GetDelivery())
	if disposition > pkgnet.SendIndeterminate || delivery > pkgnet.DeliveryReliableOrdered {
		return pkgnet.SendResult{}, false
	}
	return pkgnet.SendResult{Disposition: disposition, Delivery: delivery}, true
}
