// Copyright 2014 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// Package state provides a caching layer atop the Ethereum state trie.
package state

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/logger"
	"github.com/ethereum/go-ethereum/logger/glog"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/hashicorp/golang-lru"
)

// The starting nonce determines the default nonce when new accounts are being
// created.
var StartingNonce uint64

const maxJournalLength = 20

// StateDBs within the ethereum protocol are used to store anything
// within the merkle trie. StateDBs take care of caching and storing
// nested states. It's the general query interface to retrieve:
// * Contracts
// * Accounts
type StateDB struct {
	db            ethdb.Database
	trie          *trie.SecureTrie
	pastTries     []*trie.SecureTrie
	codeSizeCache *lru.Cache

	// This map holds 'live' objects, which will get modified
	// while processing a state transition.
	stateObjects map[common.Address]*StateObject

	// The refund counter, also used by state transitioning.
	refund *big.Int

	thash, bhash common.Hash
	txIndex      int
	logs         map[common.Hash]vm.Logs
	logSize      uint
}

// Create a new state from a given trie
func New(root common.Hash, db ethdb.Database) (*StateDB, error) {
	tr, err := trie.NewSecure(root, db)
	if err != nil {
		return nil, err
	}
	ccs, _ := lru.New(80000)
	return &StateDB{
		db:            db,
		trie:          tr,
		codeSizeCache: ccs,
		stateObjects:  make(map[common.Address]*StateObject),
		refund:        new(big.Int),
		logs:          make(map[common.Hash]vm.Logs),
	}, nil
}

// Reset clears out all emphemeral state objects from the state db, but keeps
// the underlying state trie to avoid reloading data for the next operations.
func (self *StateDB) Reset(root common.Hash) error {
	tr, err := self.openTrie(root)
	if err != nil {
		return err
	}
	*self = StateDB{
		db:            self.db,
		trie:          tr,
		pastTries:     self.pastTries,
		codeSizeCache: self.codeSizeCache,
		stateObjects:  make(map[common.Address]*StateObject),
		refund:        new(big.Int),
		logs:          make(map[common.Hash]vm.Logs),
	}
	return nil
}

// openTrie creates a trie. If a trie is available from the journal.
func (self *StateDB) openTrie(root common.Hash) (*trie.SecureTrie, error) {
	if self.trie != nil && self.trie.Hash() == root {
		return self.trie, nil
	}
	for i := len(self.pastTries) - 1; i >= 0; i-- {
		if self.pastTries[i].Hash() == root {
			tr := *self.pastTries[i]
			return &tr, nil
		}
	}
	return trie.NewSecure(root, self.db)
}

func (self *StateDB) pushTrie(t *trie.SecureTrie) {
	// Remove t's equivalent if it's already in the journal.
	root := t.Hash()
	for i := len(self.pastTries) - 1; i >= 0; i-- {
		if self.pastTries[i].Hash() == root {
			self.pastTries = append(self.pastTries[i:i], self.pastTries[i+1:]...)
			break
		}
	}
	// Insert.
	if len(self.pastTries) >= maxJournalLength {
		copy(self.pastTries, self.pastTries[1:])
		self.pastTries[len(self.pastTries)-1] = t
	} else {
		self.pastTries = append(self.pastTries, t)
	}
}

func (self *StateDB) StartRecord(thash, bhash common.Hash, ti int) {
	self.thash = thash
	self.bhash = bhash
	self.txIndex = ti
}

func (self *StateDB) AddLog(log *vm.Log) {
	log.TxHash = self.thash
	log.BlockHash = self.bhash
	log.TxIndex = uint(self.txIndex)
	log.Index = self.logSize
	self.logs[self.thash] = append(self.logs[self.thash], log)
	self.logSize++
}

func (self *StateDB) GetLogs(hash common.Hash) vm.Logs {
	return self.logs[hash]
}

func (self *StateDB) Logs() vm.Logs {
	var logs vm.Logs
	for _, lgs := range self.logs {
		logs = append(logs, lgs...)
	}
	return logs
}

func (self *StateDB) AddRefund(gas *big.Int) {
	self.refund.Add(self.refund, gas)
}

func (self *StateDB) HasAccount(addr common.Address) bool {
	return self.GetStateObject(addr) != nil
}

func (self *StateDB) Exist(addr common.Address) bool {
	return self.GetStateObject(addr) != nil
}

func (self *StateDB) GetAccount(addr common.Address) vm.Account {
	return self.GetStateObject(addr)
}

// Retrieve the balance from the given address or 0 if object not found
func (self *StateDB) GetBalance(addr common.Address) *big.Int {
	stateObject := self.GetStateObject(addr)
	if stateObject != nil {
		return stateObject.Balance()
	}

	return common.Big0
}

func (self *StateDB) GetNonce(addr common.Address) uint64 {
	stateObject := self.GetStateObject(addr)
	if stateObject != nil {
		return stateObject.Nonce()
	}

	return StartingNonce
}

func (self *StateDB) GetCode(addr common.Address) []byte {
	stateObject := self.GetStateObject(addr)
	if stateObject != nil {
		return stateObject.Code(self.db)
	}
	return nil
}

func (self *StateDB) GetCodeSize(addr common.Address) int {
	if size, ok := self.codeSizeCache.Get(addr); ok {
		return size.(int)
	}
	stateObject := self.GetStateObject(addr)
	if stateObject != nil {
		size := stateObject.CodeSize(self.db)
		self.codeSizeCache.Add(addr, size)
		return size
	}
	return 0
}

func (self *StateDB) GetState(a common.Address, b common.Hash) common.Hash {
	stateObject := self.GetStateObject(a)
	if stateObject != nil {
		return stateObject.GetState(self.db, b)
	}
	return common.Hash{}
}

func (self *StateDB) IsDeleted(addr common.Address) bool {
	stateObject := self.GetStateObject(addr)
	if stateObject != nil {
		return stateObject.remove
	}
	return false
}

/*
 * SETTERS
 */

func (self *StateDB) AddBalance(addr common.Address, amount *big.Int) {
	stateObject := self.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.AddBalance(amount)
	}
}

func (self *StateDB) SetNonce(addr common.Address, nonce uint64) {
	stateObject := self.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SetNonce(nonce)
	}
}

func (self *StateDB) SetCode(addr common.Address, code []byte) {
	stateObject := self.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SetCode(code)
	}
}

func (self *StateDB) SetState(addr common.Address, key common.Hash, value common.Hash) {
	stateObject := self.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SetState(key, value)
	}
}

func (self *StateDB) Delete(addr common.Address) bool {
	stateObject := self.GetStateObject(addr)
	if stateObject != nil {
		stateObject.MarkForDeletion()
		stateObject.data.Balance = new(big.Int)
		return true
	}

	return false
}

//
// Setting, updating & deleting state object methods
//

// Update the given state object and apply it to state trie
func (self *StateDB) UpdateStateObject(stateObject *StateObject) {
	addr := stateObject.Address()
	data, err := rlp.EncodeToBytes(stateObject)
	if err != nil {
		panic(fmt.Errorf("can't encode object at %x: %v", addr[:], err))
	}
	self.trie.Update(addr[:], data)
}

// Delete the given state object and delete it from the state trie
func (self *StateDB) DeleteStateObject(stateObject *StateObject) {
	stateObject.deleted = true

	addr := stateObject.Address()
	self.trie.Delete(addr[:])
}

// Retrieve a state object given my the address. Returns nil if not found.
func (self *StateDB) GetStateObject(addr common.Address) (stateObject *StateObject) {
	// Prefer 'live' objects.
	if obj := self.stateObjects[addr]; obj != nil {
		if obj.deleted {
			return nil
		}
		return obj
	}

	// Load the object from the database.
	enc := self.trie.Get(addr[:])
	if len(enc) == 0 {
		return nil
	}
	var data Account
	if err := rlp.DecodeBytes(enc, &data); err != nil {
		glog.Errorf("can't decode object at %x: %v", addr[:], err)
		return nil
	}
	// Insert into the live set.
	obj := NewObject(addr, data)
	self.SetStateObject(obj)
	return obj
}

func (self *StateDB) SetStateObject(object *StateObject) {
	self.stateObjects[object.Address()] = object
}

// Retrieve a state object or create a new state object if nil
func (self *StateDB) GetOrNewStateObject(addr common.Address) *StateObject {
	stateObject := self.GetStateObject(addr)
	if stateObject == nil || stateObject.deleted {
		stateObject = self.CreateStateObject(addr)
	}

	return stateObject
}

// NewStateObject create a state object whether it exist in the trie or not
func (self *StateDB) newStateObject(addr common.Address) *StateObject {
	if glog.V(logger.Core) {
		glog.Infof("(+) %x\n", addr)
	}
	obj := NewObject(addr, Account{})
	obj.dirty = true
	obj.SetNonce(StartingNonce)
	self.stateObjects[addr] = obj
	return obj
}

// Creates creates a new state object and takes ownership.
func (self *StateDB) CreateStateObject(addr common.Address) *StateObject {
	// Get previous (if any)
	so := self.GetStateObject(addr)
	// Create a new one
	newSo := self.newStateObject(addr)

	// If it existed set the balance to the new account
	if so != nil {
		newSo.data.Balance = so.data.Balance
	}

	return newSo
}

func (self *StateDB) CreateAccount(addr common.Address) vm.Account {
	return self.CreateStateObject(addr)
}

//
// Setting, copying of the state methods
//

func (self *StateDB) Copy() *StateDB {
	// ignore error - we assume state-to-be-copied always exists
	state, _ := New(common.Hash{}, self.db)
	state.trie = self.trie
	for addr, stateObject := range self.stateObjects {
		if stateObject.dirty {
			state.stateObjects[addr] = stateObject.Copy(self.db)
		}
	}

	state.refund.Set(self.refund)

	for hash, logs := range self.logs {
		state.logs[hash] = make(vm.Logs, len(logs))
		copy(state.logs[hash], logs)
	}
	state.logSize = self.logSize

	return state
}

func (self *StateDB) Set(state *StateDB) {
	self.trie = state.trie
	self.stateObjects = state.stateObjects

	self.refund = state.refund
	self.logs = state.logs
	self.logSize = state.logSize
}

func (self *StateDB) GetRefund() *big.Int {
	return self.refund
}

// IntermediateRoot computes the current root hash of the state trie.
// It is called in between transactions to get the root hash that
// goes into transaction receipts.
func (s *StateDB) IntermediateRoot() common.Hash {
	s.refund = new(big.Int)
	for _, stateObject := range s.stateObjects {
		if stateObject.dirty {
			if stateObject.remove {
				s.DeleteStateObject(stateObject)
			} else {
				stateObject.UpdateRoot(s.db)
				s.UpdateStateObject(stateObject)
			}
		}
	}
	return s.trie.Hash()
}

// DeleteSuicides flags the suicided objects for deletion so that it
// won't be referenced again when called / queried up on.
//
// DeleteSuicides should not be used for consensus related updates
// under any circumstances.
func (s *StateDB) DeleteSuicides() {
	// Reset refund so that any used-gas calculations can use
	// this method.
	s.refund = new(big.Int)
	for _, stateObject := range s.stateObjects {
		if stateObject.dirty {
			// If the object has been removed by a suicide
			// flag the object as deleted.
			if stateObject.remove {
				stateObject.deleted = true
			}
			stateObject.dirty = false
		}
	}
}

// Commit commits all state changes to the database.
func (s *StateDB) Commit() (root common.Hash, err error) {
	root, batch := s.CommitBatch()
	return root, batch.Write()
}

// CommitBatch commits all state changes to a write batch but does not
// execute the batch. It is used to validate state changes against
// the root hash stored in a block.
func (s *StateDB) CommitBatch() (root common.Hash, batch ethdb.Batch) {
	batch = s.db.NewBatch()
	root, _ = s.commit(batch)
	return root, batch
}

func (s *StateDB) commit(dbw trie.DatabaseWriter) (root common.Hash, err error) {
	s.refund = new(big.Int)

	// Commit objects to the trie.
	for _, stateObject := range s.stateObjects {
		if stateObject.remove {
			// If the object has been removed, don't bother syncing it
			// and just mark it for deletion in the trie.
			s.DeleteStateObject(stateObject)
		} else if stateObject.dirty {
			// Write any contract code associated with the state object
			if stateObject.code != nil && stateObject.dirtyCode {
				if err := dbw.Put(stateObject.CodeHash(), stateObject.code); err != nil {
					return common.Hash{}, err
				}
				stateObject.dirtyCode = false
			}
			// Write any storage changes in the state object to its storage trie.
			if err := stateObject.CommitTrie(s.db, dbw); err != nil {
				return common.Hash{}, err
			}
			// Update the object in the main account trie.
			s.UpdateStateObject(stateObject)
		}
		stateObject.dirty = false
	}
	// Write trie changes.
	root, err = s.trie.CommitTo(dbw)
	if err == nil {
		s.pushTrie(s.trie)
	}
	return root, err
}

func (self *StateDB) Refunds() *big.Int {
	return self.refund
}
