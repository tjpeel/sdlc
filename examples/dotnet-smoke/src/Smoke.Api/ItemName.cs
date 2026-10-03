namespace Smoke.Api;

public static class ItemName
{
    public static bool IsValid(string? name) =>
        !string.IsNullOrWhiteSpace(name) && name.Trim().Length <= 80;
}
