using MongoDB.Bson;
using MongoDB.Driver;
using Smoke.Api;

var builder = WebApplication.CreateBuilder(args);
var connection = builder.Configuration["Mongo:ConnectionString"]
    ?? throw new InvalidOperationException("Mongo:ConnectionString is required.");
var database = new MongoClient(connection).GetDatabase("smoke");
var items = database.GetCollection<BsonDocument>("items");
var app = builder.Build();

app.MapGet("/health", async (CancellationToken cancellation) =>
{
    await database.RunCommandAsync<BsonDocument>(
        new BsonDocument("ping", 1), cancellationToken: cancellation);
    return Results.Ok(new { status = "ready" });
});

app.MapPost("/items", async (CreateItem request, CancellationToken cancellation) =>
{
    if (!ItemName.IsValid(request.Name))
        return Results.BadRequest(new { error = "Name must contain 1 to 80 characters." });

    var id = ObjectId.GenerateNewId();
    var document = new BsonDocument { { "_id", id }, { "name", request.Name!.Trim() } };
    await items.InsertOneAsync(document, cancellationToken: cancellation);
    return Results.Created($"/items/{id}", new Item(id.ToString(), request.Name.Trim()));
});

app.MapGet("/items/{id}", async (string id, CancellationToken cancellation) =>
{
    if (!ObjectId.TryParse(id, out var objectId))
        return Results.BadRequest();
    var document = await items.Find(new BsonDocument("_id", objectId))
        .FirstOrDefaultAsync(cancellation);
    return document is null ? Results.NotFound() :
        Results.Ok(new Item(id, document["name"].AsString));
});

app.Run();

public sealed record CreateItem(string? Name);
public sealed record Item(string Id, string Name);
