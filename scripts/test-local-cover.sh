#!/bin/bash

go test -coverprofile=coverage.out ./...

TEST_EXIT_CODE=$?

if [ $TEST_EXIT_CODE -eq 0 ]; then
  echo "✅ All tests passed!"
else
  echo "❌ Tests failed with exit code: $TEST_EXIT_CODE"
fi

echo ""
echo "📊 Coverage by function:"
go tool cover -func=coverage.out

echo ""
echo "📈 Total coverage:"
go tool cover -func=coverage.out | grep total:

echo ""
echo "🌐 Generating HTML report..."
go tool cover -html=coverage.out -o /mnt/c/Users/XOMRKOB/Desktop/transcoder-service/coverage.html

echo "✅ Coverage report generated: coverage.html"

echo "🚮 Delete 'coverage.out'..."
rm coverage.out
exit $TEST_EXIT_CODE
