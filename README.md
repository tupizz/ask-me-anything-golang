### Generating from scratch

```
go generate ./...
```


### Generating migrations

```
go run ./cmd/tools/terndotenv/main.go 
```

### Creating new Migration

```
tern new --migrations ./internal/store/pgstore/migrations update_messages_table_message_column
```

### Generating code with `sqlc`

```
 sqlc generate -f ./internal/store/pgstore/sqlc.yaml
```