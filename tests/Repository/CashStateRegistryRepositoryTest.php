<?php

declare(strict_types=1);

namespace App\Tests\Repository;

use App\Entity\User;
use App\Entity\ClassCouncil\ClassRoom;
use Symfony\Component\Uid\Ulid;
use App\Entity\CashStateRegistry;
use App\Entity\ClassCouncil\ClassExpense;
use App\Entity\Payment;
use App\Repository\CashStateRegistryRepository;
use App\Tests\Functional\FunctionalTestCase;
use Brick\Money\Money;
use PHPUnit\Framework\Attributes\Group;

#[Group('functional')]
final class CashStateRegistryRepositoryTest extends FunctionalTestCase
{
    public function testRemoveDuplicatesKeepsOnlyNewestEntryPerPaymentAndExpense(): void
    {
        $user = $this->createUser('parent@example.com', 'password');
        $classRoom = $this->createClassRoom('4B');

        $duplicatedPayment = $this->persistPayment($user);
        $otherDuplicatedPayment = $this->persistPayment($user);
        $singlePayment = $this->persistPayment($user);
        $duplicatedExpense = $this->persistExpense($classRoom);
        $singleExpense = $this->persistExpense($classRoom);

        $newestPaymentEntry = $this->persistEntries($duplicatedPayment, null, 3);
        $newestOtherPaymentEntry = $this->persistEntries($otherDuplicatedPayment, null, 2);
        $singlePaymentEntry = $this->persistEntries($singlePayment, null, 1);
        $newestExpenseEntry = $this->persistEntries(null, $duplicatedExpense, 2);
        $singleExpenseEntry = $this->persistEntries(null, $singleExpense, 1);
        $this->getEntityManager()
            ->flush();

        $repository = $this->repository();
        self::assertCount(3, $repository->findByPayment($duplicatedPayment));
        self::assertCount(3, $repository->findDuplicates());

        // (3 - 1) + (2 - 1) payment entries and (2 - 1) expense entry are redundant
        self::assertSame(4, $repository->removeDuplicates());

        $this->getEntityManager()
            ->clear();
        $repository = $this->repository();

        self::assertSame([], $repository->findDuplicates());
        self::assertSame(
            [(string) $newestPaymentEntry, (string) $newestOtherPaymentEntry, (string) $singlePaymentEntry,
                (string) $newestExpenseEntry, (string) $singleExpenseEntry,
            ],
            array_map(static fn(CashStateRegistry $entry): string => (string) $entry->getId(), $repository->findBy(
                [],
                [
                    'createdAt' => 'ASC',
                ]
            )),
        );
    }

    public function testRemoveDuplicatesDoesNothingWithoutDuplicates(): void
    {
        $user = $this->createUser('parent@example.com', 'password');
        $payment = $this->persistPayment($user);
        $this->persistEntries($payment, null, 1);
        $this->getEntityManager()
            ->flush();

        self::assertSame(0, $this->repository()->removeDuplicates());
        self::assertCount(1, $this->repository()->findAll());
    }

    private function repository(): CashStateRegistryRepository
    {
        $repository = $this->getService(CashStateRegistryRepository::class);
        self::assertInstanceOf(CashStateRegistryRepository::class, $repository);

        return $repository;
    }

    private function persistPayment(User $user): Payment
    {
        $payment = new Payment($user, Money::of(100, 'PLN'));
        $this->getEntityManager()
            ->persist($payment);

        return $payment;
    }

    private function persistExpense(ClassRoom $classRoom): ClassExpense
    {
        return $this->createClassExpense($classRoom, 'Wydatek', Money::of(10, 'PLN'));
    }

    /**
     * Persists $count entries for the payment/expense with strictly increasing creation times.
     *
     * @return Ulid id of the newest entry
     */
    private function persistEntries(?Payment $payment, ?ClassExpense $expense, int $count): Ulid
    {
        static $offset = 0;

        $newest = null;
        for ($i = 0; $i < $count; $i++) {
            $entry = new CashStateRegistry(
                new \DateTimeImmutable('2024-01-01'),
                Money::of(100, 'PLN'),
                Money::of(100, 'PLN'),
                $expense instanceof ClassExpense ? 'expense' : 'income',
                $payment,
                $expense,
            );
            new \ReflectionProperty(CashStateRegistry::class, 'createdAt')->setValue(
                $entry,
                new \DateTimeImmutable('2024-02-01 00:00:00')
                    ->modify(sprintf('+%d seconds', ++$offset)),
            );
            $this->getEntityManager()
                ->persist($entry);
            $newest = $entry;
        }

        self::assertNotNull($newest);

        return $newest->getId();
    }
}
