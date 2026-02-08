# Message Contract

> 🚧 TODO: Define the NATS message format specification

## Planned Structure

```json
{
  "id": "uuid",
  "type": "task | result | event",
  "from": "tim",
  "to": "galahad",
  "timestamp": "ISO-8601",
  "payload": {},
  "metadata": {}
}
```

## TODO

- [ ] Define required fields
- [ ] Define payload schemas per task type
- [ ] Error response format
- [ ] Acknowledgment protocol
- [ ] Message versioning strategy
