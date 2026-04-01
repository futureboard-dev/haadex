package engine

import (
	"context"
	"fmt"

	"github.com/qdrant/go-client/qdrant"
)

// QdrantStore manages vector upsert and search via the Qdrant gRPC client.
type QdrantStore struct {
	client     *qdrant.Client
	collection string
	dim        uint64
}

// SearchResult is a result returned from Qdrant vector search.
type SearchResult struct {
	Name       string
	Kind       string
	File       string
	Line       int
	Content    string
	Score      float32
	ParentName string
}

// NewQdrantStore connects to Qdrant and ensures the collection exists.
func NewQdrantStore(baseURL, collection string, dim int) (*QdrantStore, error) {
	// baseURL like "http://localhost:6333" — extract host
	host, port, err := parseQdrantAddr(baseURL)
	if err != nil {
		return nil, err
	}

	client, err := qdrant.NewClient(&qdrant.Config{
		Host: host,
		Port: port,
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant dial: %w", err)
	}

	s := &QdrantStore{client: client, collection: collection, dim: uint64(dim)}
	if err := s.ensureCollection(context.Background()); err != nil {
		client.Close()
		return nil, err
	}
	return s, nil
}

func (s *QdrantStore) ensureCollection(ctx context.Context) error {
	exists, err := s.client.CollectionExists(ctx, s.collection)
	if err != nil {
		return fmt.Errorf("check collection: %w", err)
	}
	if exists {
		return nil
	}
	return s.client.CreateCollection(ctx, &qdrant.CreateCollection{
		CollectionName: s.collection,
		VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
			Size:     s.dim,
			Distance: qdrant.Distance_Cosine,
		}),
	})
}

// Upsert adds or updates a chunk in Qdrant. Uses a hash of file+name as stable ID.
func (s *QdrantStore) Upsert(chunk Chunk, vec []float32) error {
	id := stableID(chunk.File + ":" + chunk.Name + ":" + fmt.Sprint(chunk.Line))

	_, err := s.client.Upsert(context.Background(), &qdrant.UpsertPoints{
		CollectionName: s.collection,
		Points: []*qdrant.PointStruct{
			{
				Id:      qdrant.NewIDNum(id),
				Vectors: qdrant.NewVectors(vec...),
				Payload: map[string]*qdrant.Value{
					"name":        {Kind: &qdrant.Value_StringValue{StringValue: chunk.Name}},
					"kind":        {Kind: &qdrant.Value_StringValue{StringValue: chunk.Kind}},
					"file":        {Kind: &qdrant.Value_StringValue{StringValue: chunk.File}},
					"line":        {Kind: &qdrant.Value_IntegerValue{IntegerValue: int64(chunk.Line)}},
					"content":     {Kind: &qdrant.Value_StringValue{StringValue: chunk.Content}},
					"parent_name": {Kind: &qdrant.Value_StringValue{StringValue: chunk.ParentName}},
				},
			},
		},
	})
	return err
}

// Search performs a nearest-neighbor search and returns matching chunks.
func (s *QdrantStore) Search(vec []float32, limit int) ([]SearchResult, error) {
	lim := uint64(limit)
	results, err := s.client.Query(context.Background(), &qdrant.QueryPoints{
		CollectionName: s.collection,
		Query:          qdrant.NewQuery(vec...),
		Limit:          &lim,
		WithPayload:    qdrant.NewWithPayload(true),
	})
	if err != nil {
		return nil, err
	}

	var out []SearchResult
	for _, r := range results {
		p := r.Payload
		out = append(out, SearchResult{
			Name:       stringVal(p, "name"),
			Kind:       stringVal(p, "kind"),
			File:       stringVal(p, "file"),
			Line:       int(intVal(p, "line")),
			Content:    stringVal(p, "content"),
			Score:      r.Score,
			ParentName: stringVal(p, "parent_name"),
		})
	}
	return out, nil
}

// DeleteByFile removes all points whose payload `file` field matches the given path.
func (s *QdrantStore) DeleteByFile(file string) error {
	_, err := s.client.Delete(context.Background(), &qdrant.DeletePoints{
		CollectionName: s.collection,
		Points: &qdrant.PointsSelector{
			PointsSelectorOneOf: &qdrant.PointsSelector_Filter{
				Filter: &qdrant.Filter{
					Must: []*qdrant.Condition{{
						ConditionOneOf: &qdrant.Condition_Field{
							Field: &qdrant.FieldCondition{
								Key: "file",
								Match: &qdrant.Match{
									MatchValue: &qdrant.Match_Keyword{
										Keyword: file,
									},
								},
							},
						},
					}},
				},
			},
		},
	})
	return err
}

// ResetCollection drops and recreates the collection, clearing all vectors.
func (s *QdrantStore) ResetCollection() error {
	ctx := context.Background()
	exists, err := s.client.CollectionExists(ctx, s.collection)
	if err != nil {
		return fmt.Errorf("check collection: %w", err)
	}
	if exists {
		if err := s.client.DeleteCollection(ctx, s.collection); err != nil {
			return fmt.Errorf("delete collection: %w", err)
		}
	}
	return s.ensureCollection(ctx)
}

// Close closes the gRPC connection.
func (s *QdrantStore) Close() error {
	return s.client.Close()
}

// --- helpers ---

func parseQdrantAddr(url string) (host string, port int, err error) {
	// strip scheme
	s := url
	for _, prefix := range []string{"https://", "http://"} {
		if len(s) > len(prefix) && s[:len(prefix)] == prefix {
			s = s[len(prefix):]
		}
	}
	// split host:port
	host = s
	port = 6334 // default gRPC port
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			host = s[:i]
			fmt.Sscanf(s[i+1:], "%d", &port)
			// map REST port 6333 -> gRPC port 6334
			if port == 6333 {
				port = 6334
			}
			break
		}
	}
	return host, port, nil
}

func stableID(key string) uint64 {
	// FNV-1a 64-bit hash
	var h uint64 = 14695981039346656037
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	return h
}

func stringVal(p map[string]*qdrant.Value, key string) string {
	v, ok := p[key]
	if !ok || v == nil {
		return ""
	}
	sv, ok := v.Kind.(*qdrant.Value_StringValue)
	if !ok {
		return ""
	}
	return sv.StringValue
}

func intVal(p map[string]*qdrant.Value, key string) int64 {
	v, ok := p[key]
	if !ok || v == nil {
		return 0
	}
	iv, ok := v.Kind.(*qdrant.Value_IntegerValue)
	if !ok {
		return 0
	}
	return iv.IntegerValue
}
