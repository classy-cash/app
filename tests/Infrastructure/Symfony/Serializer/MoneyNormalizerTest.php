<?php

declare(strict_types=1);

namespace App\Tests\Infrastructure\Symfony\Serializer;

use App\Infrastructure\Symfony\Serializer\MoneyNormalizer;
use Brick\Math\Exception\MathException;
use Brick\Money\Exception\UnknownCurrencyException;
use Brick\Money\Money;
use PHPUnit\Framework\Attributes\DataProvider;
use PHPUnit\Framework\Attributes\Group;
use PHPUnit\Framework\TestCase;

#[Group('unit')]
final class MoneyNormalizerTest extends TestCase
{
    public function testDenormalizesAmountAndCurrency(): void
    {
        $money = new MoneyNormalizer()
            ->denormalize([
                'amount' => '12.50',
                'currency' => 'PLN',
            ], Money::class);

        self::assertTrue($money->isEqualTo(Money::of('12.50', 'PLN')));
        self::assertSame('PLN', $money->getCurrency()->getCurrencyCode());
    }

    #[DataProvider('invalidPayloads')]
    public function testDenormalizeRejectsInvalidPayload(mixed $data): void
    {
        $this->expectException(\InvalidArgumentException::class);
        $this->expectExceptionMessage('Invalid data for Money denormalization');

        new MoneyNormalizer()
            ->denormalize($data, Money::class);
    }

    public function testDenormalizeDoesNotSwallowMoneyErrors(): void
    {
        $normalizer = new MoneyNormalizer();

        try {
            $normalizer->denormalize([
                'amount' => 'twelve',
                'currency' => 'PLN',
            ], Money::class);
            self::fail('A non-numeric amount must not be denormalized.');
        } catch (MathException) {
            $this->addToAssertionCount(1);
        }

        $this->expectException(UnknownCurrencyException::class);
        $normalizer->denormalize([
            'amount' => '1.00',
            'currency' => 'XXY',
        ], Money::class);
    }

    public function testSupportsDenormalizationOnlyForWellFormedMoneyPayloads(): void
    {
        $normalizer = new MoneyNormalizer();
        $payload = [
            'amount' => '1.00',
            'currency' => 'PLN',
        ];

        self::assertTrue($normalizer->supportsDenormalization($payload, Money::class));
        self::assertFalse($normalizer->supportsDenormalization($payload, \stdClass::class));
        self::assertFalse($normalizer->supportsDenormalization('1.00 PLN', Money::class));
        self::assertFalse($normalizer->supportsDenormalization([
            'amount' => 1,
            'currency' => 'PLN',
        ], Money::class));
    }

    public function testNormalizesMoneyAndIgnoresOtherValues(): void
    {
        $normalizer = new MoneyNormalizer();

        self::assertSame([
            'amount' => '99.90',
            'currency' => 'PLN',
        ], $normalizer->normalize(Money::of('99.9', 'PLN')));
        self::assertNull($normalizer->normalize('99.90 PLN'));
    }

    public function testSupportsNormalizationOnlyForMoney(): void
    {
        $normalizer = new MoneyNormalizer();

        self::assertTrue($normalizer->supportsNormalization(Money::of(1, 'PLN')));
        self::assertFalse($normalizer->supportsNormalization(new \stdClass()));
        self::assertSame([
            Money::class => true,
        ], $normalizer->getSupportedTypes(null));
    }

    public function testNormalizedMoneyRoundTrips(): void
    {
        $normalizer = new MoneyNormalizer();
        $original = Money::of('1234.56', 'PLN');

        $normalized = $normalizer->normalize($original);
        self::assertIsArray($normalized);

        self::assertTrue($normalizer->denormalize($normalized, Money::class)->isEqualTo($original));
    }

    /**
     * @return iterable<string, array{mixed}>
     */
    public static function invalidPayloads(): iterable
    {
        yield 'not an array' => ['12.50 PLN'];
        yield 'null' => [null];
        yield 'empty array' => [[]];
        yield 'missing currency' => [[
            'amount' => '1.00',
        ]];
        yield 'missing amount' => [[
            'currency' => 'PLN',
        ]];
        yield 'null amount' => [[
            'amount' => null,
            'currency' => 'PLN',
        ]];
        yield 'integer amount' => [[
            'amount' => 100,
            'currency' => 'PLN',
        ]];
        yield 'non-string currency' => [[
            'amount' => '1.00',
            'currency' => 985,
        ]];
    }
}
