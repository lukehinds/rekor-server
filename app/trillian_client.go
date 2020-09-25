/*
Copyright © 2020 Luke Hinds <lhinds@redhat.com>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package app

import (
	"context"
	"fmt"
	"time"

	"github.com/golang/protobuf/ptypes"
	"github.com/projectrekor/rekor-server/logging"

	"github.com/google/trillian"
	"github.com/google/trillian/client"
	"github.com/google/trillian/crypto/keyspb"
	"github.com/google/trillian/crypto/sigpb"
	"github.com/google/trillian/merkle"
	"github.com/google/trillian/merkle/rfc6962"
	"github.com/google/trillian/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type trillianclient struct {
	lc     *client.LogClient
	client trillian.TrillianLogClient
	logID  int64
	ctx    context.Context
}

type Response struct {
	status         codes.Code
	getLeafResult  *trillian.GetLeavesByHashResponse
	getProofResult *trillian.GetInclusionProofByHashResponse
}

func serverInstance(client trillian.TrillianLogClient, tLogID int64) *trillianclient {
	return &trillianclient{
		client: client,
		logID:  tLogID,
	}
}

func (s *trillianclient) root() (types.LogRootV1, error) {
	rqst := &trillian.GetLatestSignedLogRootRequest{
		LogId: s.logID,
	}
	resp, err := s.client.GetLatestSignedLogRoot(context.Background(), rqst)
	if err != nil {
		return types.LogRootV1{}, err
	}
	var root types.LogRootV1
	if err := root.UnmarshalBinary(resp.SignedLogRoot.LogRoot); err != nil {
		return types.LogRootV1{}, err
	}
	return root, nil
}

func (s *trillianclient) getProof(byteValue []byte, tLogID int64) (*Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	root, err := s.root()
	if err != nil {
		return &Response{}, err
	}

	// logging.Logger.Infof("Root hash: %x", root.RootHash)

	hasher := rfc6962.DefaultHasher
	leafHash := hasher.HashLeaf(byteValue)

	resp, err := s.client.GetInclusionProofByHash(ctx,
		&trillian.GetInclusionProofByHashRequest{
			LogId:    tLogID,
			LeafHash: leafHash,
			TreeSize: int64(root.TreeSize),
		})

	v := merkle.NewLogVerifier(rfc6962.DefaultHasher)

	if resp != nil {
		for i, proof := range resp.Proof {
			hashes := proof.GetHashes()
			for j, hash := range hashes {
				logging.Logger.Infof("Proof[%d],hash[%d] == %x\n", i, j, hash)
			}
			if err := v.VerifyInclusionProof(proof.LeafIndex, int64(root.TreeSize), hashes, root.RootHash, leafHash); err != nil {
				return &Response{}, err
			}
		}
	}

	return &Response{
		status:         status.Code(err),
		getProofResult: resp,
	}, nil
}

func (s *trillianclient) addLeaf(byteValue []byte, tLogID int64) (*Response, error) {
	leaf := &trillian.LogLeaf{
		LeafValue: byteValue,
	}
	rqst := &trillian.QueueLeafRequest{
		LogId: tLogID,
		Leaf:  leaf,
	}
	resp, err := s.client.QueueLeaf(context.Background(), rqst)
	if err != nil {
		fmt.Println(err)
	}

	resultCode := codes.Code(resp.QueuedLeaf.GetStatus().GetCode())

	return &Response{
		status: resultCode,
	}, nil
}

func (s *trillianclient) getLeaf(byteValue []byte, tlog_id int64) (*Response, error) {
	hasher := rfc6962.DefaultHasher
	leafHash := hasher.HashLeaf(byteValue)

	rqst := &trillian.GetLeavesByHashRequest{
		LogId:    tlog_id,
		LeafHash: [][]byte{leafHash},
	}

	resp, err := s.client.GetLeavesByHash(context.Background(), rqst)
	if err != nil {
		logging.Logger.Fatal(err)
	}

	return &Response{
		status:        status.Code(err),
		getLeafResult: resp,
	}, nil
}

func createAndInitTree(ctx context.Context, adminClient trillian.TrillianAdminClient, logClient trillian.TrillianLogClient) (*trillian.Tree, error) {
	// First look for and use an existing tree
	trees, err := adminClient.ListTrees(ctx, &trillian.ListTreesRequest{})
	if err != nil {
		return nil, err
	}

	for _, t := range trees.Tree {
		if t.TreeType == trillian.TreeType_LOG {
			return t, nil
		}
	}

	// Otherwise create and initialize one
	t, err := adminClient.CreateTree(ctx, &trillian.CreateTreeRequest{
		Tree: &trillian.Tree{
			TreeType:           trillian.TreeType_LOG,
			HashStrategy:       trillian.HashStrategy_RFC6962_SHA256,
			HashAlgorithm:      sigpb.DigitallySigned_SHA256,
			SignatureAlgorithm: sigpb.DigitallySigned_ECDSA,
			TreeState:          trillian.TreeState_ACTIVE,
			MaxRootDuration:    ptypes.DurationProto(time.Hour),
		},
		KeySpec: &keyspb.Specification{
			Params: &keyspb.Specification_EcdsaParams{
				EcdsaParams: &keyspb.Specification_ECDSA{},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	if err := client.InitLog(ctx, t, logClient); err != nil {
		return nil, err
	}
	return t, nil
}

func createAndInitMap(ctx context.Context, adminClient trillian.TrillianAdminClient, mapClient trillian.TrillianMapClient) (*trillian.Tree, error) {
	// First look for and use an existing tree
	trees, err := adminClient.ListTrees(ctx, &trillian.ListTreesRequest{})
	if err != nil {
		return nil, err
	}

	for _, t := range trees.Tree {
		if t.TreeType == trillian.TreeType_MAP {
			return t, nil
		}
	}

	// Otherwise create and initialize one
	t, err := adminClient.CreateTree(ctx, &trillian.CreateTreeRequest{
		Tree: &trillian.Tree{
			TreeType:           trillian.TreeType_MAP,
			HashStrategy:       trillian.HashStrategy_CONIKS_SHA256,
			HashAlgorithm:      sigpb.DigitallySigned_SHA256,
			SignatureAlgorithm: sigpb.DigitallySigned_ECDSA,
			TreeState:          trillian.TreeState_ACTIVE,
			MaxRootDuration:    ptypes.DurationProto(time.Hour),
		},
		KeySpec: &keyspb.Specification{
			Params: &keyspb.Specification_EcdsaParams{
				EcdsaParams: &keyspb.Specification_ECDSA{},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	if err := client.InitMap(ctx, t, mapClient); err != nil {
		return nil, err
	}
	return t, nil
}
