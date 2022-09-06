package avail

import (
	"fmt"
	"log"

	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/maticnetwork/avail-settlement/pkg/avail"
)

type ValidatorSet []types.Address

type dataHandler struct {
	blockchain *blockchain.Blockchain
}

func (dh *dataHandler) HandleData(bs []byte) error {
	log.Printf("block handler: received batch w/ %d bytes\n", len(bs))

	block := types.Block{}
	if err := block.UnmarshalRLP(bs); err != nil {
		return err
	}

	if err := dh.blockchain.VerifyFinalizedBlock(&block); err != nil {
		return fmt.Errorf("unable to verify block, %w", err)
	}

	if err := dh.blockchain.WriteBlock(&block); err != nil {
		return fmt.Errorf("failed to write block while bulk syncing: %w", err)
	}

	log.Printf("Received block header: %+v \n", block.Header)
	log.Printf("Received block transactions: %+v \n", block.Transactions)

	return nil
}
func (dh *dataHandler) HandleError(err error) {
	log.Printf("block handler: error %#v\n", err)
}

func (d *Avail) runValidator() {
	d.logger.Info("validator started")

	// consensus always starts in SyncState mode in case it needs
	// to sync with Avail and/or other nodes.
	d.setState(SyncState)

	handler := &dataHandler{blockchain: d.blockchain}

	watcher, err := avail.NewBlockDataWatcher(d.availClient, avail.BridgeAppID, handler)
	if err != nil {
		panic("couldn't create new avail block watcher: " + err.Error())
	}

	if err := watcher.Start(); err != nil {
		panic("watcher start failed: " + err.Error())
	}

	defer watcher.Stop()

	// TODO: Figure out where do we need state cycle and how to implement it.
	// Current version only starts the cycles for the future, doing nothing with it.
	for {
		select {
		case <-d.closeCh:
			return
		default: // Default is here because we would block until we receive something in the closeCh
		}

		// Start the state machine loop
		d.runValidatorCycle()
	}
}

func (d *Avail) runValidatorCycle() {
	// Based on the current state, execute the corresponding section
	switch d.getState() {
	case AcceptState:
		d.runAcceptState()

	case ValidateState:
		d.runValidateState()

	case SyncState:
		d.runSyncState()
	}
}

func (d *Avail) runSyncState() {
	if !d.isState(SyncState) {
		return
	}
}

func (d *Avail) runValidateState() {
	if !d.isState(ValidateState) {
		return
	}
}

func (d *Avail) runAcceptState() {
	if !d.isState(AcceptState) {
		return
	}
}