#!/bin/bash

echo "Testing beta"

./count --channel=status -mailserver="enode://7aa648d6e855950b2e3d3bf220c496e0cae4adfddef3e1e6062e6b177aec93bc6cdcf1282cb40d1656932ebfdd565729da440368d7c4da7dbd4d004b1ac02bf8@206.189.243.169:443"

echo "Testing staging"

./count --channel=status -mailserver="enode://69f72baa7f1722d111a8c9c68c39a31430e9d567695f6108f31ccb6cd8f0adff4991e7fdca8fa770e75bc8a511a87d24690cbc80e008175f40c157d6f6788d48@206.189.240.16:443"


echo "Testing test"
./count --channel=status -mailserver="enode://e4865fe6c2a9c1a563a6447990d8e9ce672644ae3e08277ce38ec1f1b690eef6320c07a5d60c3b629f5d4494f93d6b86a745a0bf64ab295bbf6579017adc6ed8@206.189.243.161:443"



