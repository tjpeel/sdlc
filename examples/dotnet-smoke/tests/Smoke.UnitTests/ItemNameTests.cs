using Smoke.Api;
using Xunit;

public class ItemNameTests
{
    [Theory]
    [InlineData(null)]
    [InlineData("")]
    [InlineData("   ")]
    public void RejectsEmptyName(string? name) => Assert.False(ItemName.IsValid(name));

    [Fact]
    public void ChecksTrimmedLengthBoundary()
    {
        Assert.True(ItemName.IsValid(" " + new string('a', 80) + " "));
        Assert.False(ItemName.IsValid(new string('a', 81)));
    }
}
