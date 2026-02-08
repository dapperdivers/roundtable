# NATS Topic Conventions

> 🚧 TODO: Define the topic hierarchy

## Planned Hierarchy

```
roundtable.knight.<name>.task      # Task assignments
roundtable.knight.<name>.result    # Task results
roundtable.knight.<name>.status    # Health/status updates
roundtable.broadcast               # All-knights announcements
roundtable.event.<type>            # System events
```

## TODO

- [ ] Finalize topic naming
- [ ] Define stream/consumer configuration
- [ ] Retention policies
- [ ] Access control per knight
