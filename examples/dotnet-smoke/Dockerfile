FROM mcr.microsoft.com/dotnet/sdk:10.0.401 AS build
WORKDIR /source
COPY . .
RUN dotnet restore Smoke.slnx
RUN dotnet publish src/Smoke.Api/Smoke.Api.csproj --no-restore -c Release -o /app

FROM build AS tests
CMD ["dotnet", "test", "tests/Smoke.IntegrationTests/Smoke.IntegrationTests.csproj", "--no-restore", "-c", "Release"]

FROM mcr.microsoft.com/dotnet/aspnet:10.0.12 AS api
WORKDIR /app
COPY --from=build /app .
ENV ASPNETCORE_HTTP_PORTS=8080
USER $APP_UID
ENTRYPOINT ["dotnet", "Smoke.Api.dll"]
