package mongodb_stream_benthos

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Jeffail/benthos/v3/public/service"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var mongoStreamConfigSpec = service.NewConfigSpec().
	Summary("Creates an input that generates mongo stream").
	Field(service.NewStringField("uri")).
	Field(service.NewStringField("database")).
	Field(service.NewStringField("collection")).
	Field(service.NewBoolField("stream_snapshot"))

type mongoStreamInput struct {
	uri            string
	database       string
	collection     string
	client         *mongo.Client
	stream         chan map[string]interface{}
	streamSnapshot bool
	context        context.Context
	cancelFunc     context.CancelFunc
}

func newMongoStreamInput(conf *service.ParsedConfig) (service.Input, error) {
	var (
		uri            string
		database       string
		collection     string
		streamSnapshot bool
	)

	uri, err := conf.FieldString("uri")

	if err != nil {
		return nil, err
	}

	database, err = conf.FieldString("database")

	if err != nil {
		return nil, err
	}

	collection, err = conf.FieldString("collection")

	if err != nil {
		return nil, err
	}

	streamSnapshot, err = conf.FieldBool("stream_snapshot")

	if err != nil {
		return nil, err
	}

	return service.AutoRetryNacks(&mongoStreamInput{
		uri:            uri,
		database:       database,
		collection:     collection,
		streamSnapshot: streamSnapshot,
		stream:         make(chan map[string]interface{}),
	}), nil
}

func init() {
	err := service.RegisterInput(
		"mongodb_stream",
		mongoStreamConfigSpec,
		func(conf *service.ParsedConfig, mgr *service.Resources) (service.Input, error) {
			return newMongoStreamInput(conf)
		},
	)

	if err != nil {
		panic(err)
	}
}

func (m *mongoStreamInput) Connect(ctx context.Context) error {
	client, err := mongo.Connect(context.TODO(), options.Client().ApplyURI(m.uri))

	m.client = client

	if err != nil {
		panic(err)
	}

	database := client.Database(m.database)

	collection := database.Collection(m.collection)

	m.context, m.cancelFunc = context.WithCancel(context.Background())
	if m.streamSnapshot {
		go m.takeSnapshot(collection)
	} else {
		stream, err := collection.Watch(ctx, mongo.Pipeline{}, options.ChangeStream().SetFullDocument(options.UpdateLookup))

		if err != nil {
			panic(err)
		}

		go m.iterateChangeStream(stream)
	}

	return nil
}

func (m *mongoStreamInput) takeSnapshot(c *mongo.Collection) {
	cursor, err := c.Find(m.context, bson.D{})
	fmt.Println("Snapshot")
	if err != nil {
		panic(err)
	}

	m.iterateCursor(cursor)

	stream, err := c.Watch(m.context, mongo.Pipeline{}, options.ChangeStream().SetFullDocument(options.UpdateLookup))

	if err != nil {
		panic(err)
	}

	m.iterateChangeStream(stream)
}

func (m *mongoStreamInput) iterateCursor(cursor *mongo.Cursor) {
	defer cursor.Close(context.TODO())

	for cursor.Next(context.TODO()) {
		var data bson.M

		if err := cursor.Decode(&data); err != nil {
			panic(err)
		}

		m.processMessage(data)
	}

	if err := cursor.Err(); err != nil {
		panic(err)
	}
}

func (m *mongoStreamInput) iterateChangeStream(stream *mongo.ChangeStream) {
	defer stream.Close(context.TODO())

	for stream.Next(context.TODO()) {
		var data bson.M

		if err := stream.Decode(&data); err != nil {
			panic(err)
		}

		m.processMessage(data)
	}
}

func (m *mongoStreamInput) processMessage(data map[string]interface{}) {
	event := make(map[string]interface{})

	if operation, ok := data["operationType"]; ok {
		switch operation {
		case "delete":
			event["action"] = operation
			event["database"] = m.database
			event["collection"] = m.collection
			event["data"] = data["documentKey"]
		default:
			event["action"] = operation
			event["database"] = m.database
			event["collection"] = m.collection
			event["data"] = data["fullDocument"]
		}
	} else {
		event["action"] = "insert"
		event["database"] = m.database
		event["collection"] = m.collection
		event["data"] = data
	}

	m.stream <- event
}

func (m *mongoStreamInput) Close(ctx context.Context) error {
	if m.client != nil {
		return m.client.Disconnect(context.TODO())
	}

	m.cancelFunc()
	return nil
}

func (m *mongoStreamInput) Read(ctx context.Context) (*service.Message, service.AckFunc, error) {
	snapshotMessage := <-m.stream
	messageBodyEncoded, _ := json.Marshal(snapshotMessage["data"])
	createdMessage := service.NewMessage(messageBodyEncoded)
	createdMessage.MetaSet("table", snapshotMessage["collection"].(string))
	createdMessage.MetaSet("schema", snapshotMessage["database"].(string))
	createdMessage.MetaSet("event", snapshotMessage["action"].(string))

	return createdMessage, func(ctx context.Context, err error) error {
		return nil
	}, nil
}
