# Supply independently reviewed .NET 10 Linux image digests at build time.
ARG SDK_IMAGE
ARG RUNTIME_IMAGE
FROM ${SDK_IMAGE} AS build
WORKDIR /src
COPY ErGateway/ ErGateway/
COPY ErGateway.Tests/ ErGateway.Tests/
RUN dotnet run --project ErGateway.Tests -c Release --no-launch-profile
RUN dotnet publish ErGateway -c Release --no-restore -o /out -p:UseAppHost=false

FROM ${RUNTIME_IMAGE} AS runtime
WORKDIR /app
COPY --from=build --chown=1654:1654 /out/ ./
USER 1654:1654
ENV ASPNETCORE_URLS=https://+:8443 DOTNET_EnableDiagnostics=0
EXPOSE 8443
ENTRYPOINT ["dotnet", "ErGateway.dll"]
