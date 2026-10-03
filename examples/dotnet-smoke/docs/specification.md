# Disposable item-count change

This public specification is a fixture, not a work ticket for the SDLC repository.
Copy it with the sample ticket into an ignored work folder in a disposable clone.

Add `GET /items/count` to the sample API. Return HTTP 200 and JSON
`{"count": <number>}` containing the number of documents in Mongo's `items`
collection. The count must reflect successful creates; rejected creates do not
change it. Use the existing Mongo connection and database. Do not change the
existing create, read, validation or health contracts.

The route must work for an empty database and after two successful creates.
Integration tests must exercise the running API and real disposable Mongo.
Keep unit and integration check commands separate. No provider connection,
authentication, deployment or publication is part of this change.
