package staking

import (
	"math/big"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/maticnetwork/avail-settlement/pkg/test"
	"github.com/test-go/testify/assert"
)

func TestMinValidatorRater(t *testing.T) {
	tAssert := assert.New(t)

	// TODO: Check if verifier is even necessary to be applied. For now skipping it.
	executor, blockchain := test.NewBlockchain(t, NewVerifier(new(DumbActiveParticipants), hclog.Default()), getGenesisBasePath())
	tAssert.NotNil(executor)
	tAssert.NotNil(blockchain)

	balance := big.NewInt(0).Mul(big.NewInt(1000), ETH)
	coinbaseAddr, coinbaseSignKey := test.NewAccount(t)
	test.DepositBalance(t, coinbaseAddr, balance, blockchain, executor)

	validatorRater := NewValidatorsRater(blockchain, executor, hclog.Default())
	minimum, err := validatorRater.CurrentMinimum()
	tAssert.NoError(err)
	tAssert.Equal(minimum.Int64(), big.NewInt(0).Int64())

	err = validatorRater.SetMinimum(big.NewInt(10), coinbaseSignKey)
	tAssert.NoError(err)

	nextMinimum, err := validatorRater.CurrentMinimum()
	tAssert.NoError(err)
	tAssert.Equal(nextMinimum.Int64(), big.NewInt(10).Int64())
}

func TestMaxValidatorRater(t *testing.T) {
	tAssert := assert.New(t)

	// TODO: Check if verifier is even necessary to be applied. For now skipping it.
	executor, blockchain := test.NewBlockchain(t, NewVerifier(new(DumbActiveParticipants), hclog.Default()), getGenesisBasePath())
	tAssert.NotNil(executor)
	tAssert.NotNil(blockchain)

	balance := big.NewInt(0).Mul(big.NewInt(1000), ETH)
	coinbaseAddr, coinbaseSignKey := test.NewAccount(t)
	test.DepositBalance(t, coinbaseAddr, balance, blockchain, executor)

	validatorRater := NewValidatorsRater(blockchain, executor, hclog.Default())
	maximum, err := validatorRater.CurrentMaximum()
	tAssert.NoError(err)
	tAssert.Equal(maximum.Int64(), big.NewInt(0).Int64())

	err = validatorRater.SetMaximum(big.NewInt(10), coinbaseSignKey)
	tAssert.NoError(err)

	nextMaximum, err := validatorRater.CurrentMaximum()
	tAssert.NoError(err)
	tAssert.Equal(nextMaximum.Int64(), big.NewInt(10).Int64())
}