{ lib, buildPythonPackage, fetchPypi, httpx2, packaging, pydantic, pydantic-core
, tenacity, truststore, typing-extensions }:

# Not available in the pinned nixpkgs. Only this SDK is packaged locally;
# its complete dependency graph uses the same nixpkgs libraries as other ops.
buildPythonPackage rec {
  pname = "typesafe-sdk";
  version = "0.7.4";
  format = "wheel";

  src = fetchPypi {
    pname = "typesafe_sdk";
    inherit version format;
    python = "py3";
    dist = "py3";
    hash = "sha256-Yc8/L+naILaMPT/i+3a7svNfjEqaZaa4HEOlNdKlMhA=";
  };

  dependencies = [
    httpx2 packaging pydantic pydantic-core tenacity truststore typing-extensions
  ];
  pythonImportsCheck = [ "typesafe_sdk" ];
  meta = {
    description = "Python SDK for TypeSafe's structured inference API";
    homepage = "https://docs.typesafe.ai/sdk/python";
    license = lib.licenses.mit;
  };
}
