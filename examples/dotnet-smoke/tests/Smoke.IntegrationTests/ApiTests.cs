using System.Net;
using System.Net.Http.Json;
using Xunit;

public class ApiTests
{
    [Fact]
    public async Task CreatesAndReadsItemThroughMongo()
    {
        Assert.False(File.Exists("/source/.env"), "The host .env must not be baked into the test image.");
        var directMarker = Environment.GetEnvironmentVariable("SMOKE_ENV_FILE_MARKER");
        var interpolatedMarker = Environment.GetEnvironmentVariable("SMOKE_COMPOSE_MARKER");
        Assert.True(directMarker?.StartsWith("fake-dotenv-", StringComparison.Ordinal) == true
            && string.Equals(directMarker, interpolatedMarker, StringComparison.Ordinal),
            "Compose must inject the disposable marker through env_file and interpolation.");
        var endpoint = Environment.GetEnvironmentVariable("SMOKE_API_URL")
            ?? throw new InvalidOperationException("Run scripts/integration.py against the dedicated Docker daemon.");
        using var client = new HttpClient { BaseAddress = new Uri(endpoint), Timeout = TimeSpan.FromSeconds(5) };
        var ready = false;
        for (var attempt = 0; attempt < 60; attempt++)
        {
            try
            {
                using var health = await client.GetAsync("/health");
                if (health.IsSuccessStatusCode) { ready = true; break; }
            }
            catch (HttpRequestException) { }
            catch (TaskCanceledException) { }
            await Task.Delay(500);
        }
        Assert.True(ready, "API and Mongo did not become ready.");

        using var created = await client.PostAsJsonAsync("/items", new { name = "  fixture item  " });
        Assert.Equal(HttpStatusCode.Created, created.StatusCode);
        Assert.NotNull(created.Headers.Location);
        using var fetched = await client.GetAsync(created.Headers.Location);
        Assert.Equal(HttpStatusCode.OK, fetched.StatusCode);
        var item = await fetched.Content.ReadFromJsonAsync<Item>();
        Assert.NotNull(item);
        Assert.Equal("fixture item", item.Name);
        Assert.False(string.IsNullOrEmpty(item.Id));

        using var invalid = await client.PostAsJsonAsync("/items", new { name = " " });
        Assert.Equal(HttpStatusCode.BadRequest, invalid.StatusCode);
        using var missing = await client.GetAsync("/items/000000000000000000000000");
        Assert.Equal(HttpStatusCode.NotFound, missing.StatusCode);
    }

    private sealed record Item(string Id, string Name);
}
