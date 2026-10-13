<?php

declare(strict_types=1);

namespace App\Repository;

use App\Entity\CashStateRegistry;
use App\Entity\ClassCouncil\ClassExpense;
use App\Entity\Payment;
use Doctrine\Bundle\DoctrineBundle\Repository\ServiceEntityRepository;
use Doctrine\Persistence\ManagerRegistry;

/**
 * @extends ServiceEntityRepository<CashStateRegistry>
 */
class CashStateRegistryRepository extends ServiceEntityRepository
{
    public function __construct(ManagerRegistry $registry)
    {
        parent::__construct($registry, CashStateRegistry::class);
    }

    /**
     * @return CashStateRegistry[]
     */
    public function findByPayment(Payment $payment): array
    {
        return $this->findBy([
            'payment' => $payment,
        ]);
    }

    /**
     * @return CashStateRegistry[]
     */
    public function findByExpense(ClassExpense $expense): array
    {
        return $this->findBy([
            'expense' => $expense,
        ]);
    }

    /**
     * Find all registry entries after a given date
     *
     * @return CashStateRegistry[]
     */
    public function findAfterDate(\DateTimeImmutable $date): array
    {
        /** @var CashStateRegistry[] */
        return $this->createQueryBuilder('csr')
            ->where('csr.transactionDate > :date')
            ->setParameter('date', $date)
            ->orderBy('csr.transactionDate', 'ASC')
            ->getQuery()
            ->getResult();
    }

    /**
     * Find the most recent registry entry before a given date
     */
    public function findLastBeforeDate(\DateTimeImmutable $date): ?CashStateRegistry
    {
        /** @var CashStateRegistry|null */
        return $this->createQueryBuilder('csr')
            ->where('csr.transactionDate < :date')
            ->setParameter('date', $date)
            ->orderBy('csr.transactionDate', 'DESC')
            ->setMaxResults(1)
            ->getQuery()
            ->getOneOrNullResult();
    }

    /**
     * Find all registry entries ordered by transaction date
     *
     * @return CashStateRegistry[]
     */
    public function findAllOrdered(): array
    {
        /** @var CashStateRegistry[] */
        return $this->createQueryBuilder('csr')
            ->orderBy('csr.transactionDate', 'ASC')
            ->getQuery()
            ->getResult();
    }

    /**
     * Find all registry entries ordered by transaction date (most recent first)
     *
     * @return CashStateRegistry[]
     */
    public function findAllOrderedDesc(): array
    {
        /** @var CashStateRegistry[] */
        return $this->createQueryBuilder('csr')
            ->orderBy('csr.transactionDate', 'DESC')
            ->getQuery()
            ->getResult();
    }

    /**
     * Find the most recent registry entry
     */
    public function findLatest(): ?CashStateRegistry
    {
        /** @var CashStateRegistry|null */
        return $this->createQueryBuilder('csr')
            ->orderBy('csr.transactionDate', 'DESC')
            ->setMaxResults(1)
            ->getQuery()
            ->getOneOrNullResult();
    }

    /**
     * Find registry entries for pagination
     *
     * @param int $page Page number (1-based)
     * @param int $limit Items per page
     * @return CashStateRegistry[]
     */
    public function findPaginated(int $page, int $limit): array
    {
        $offset = ($page - 1) * $limit;
        /** @var CashStateRegistry[] */
        return $this->createQueryBuilder('csr')
            ->orderBy('csr.transactionDate', 'DESC')
            ->setFirstResult($offset)
            ->setMaxResults($limit)
            ->getQuery()
            ->getResult();
    }

    /**
     * Delete all registry entries after a given date
     */
    public function deleteAfterDate(\DateTimeImmutable $date): int
    {
        /** @var int */
        return $this->createQueryBuilder('csr')
            ->delete()
            ->where('csr.transactionDate > :date')
            ->setParameter('date', $date)
            ->getQuery()
            ->execute();
    }

    /**
     * Find a registry entry by payment
     */
    public function findOneByPayment(Payment $payment): ?CashStateRegistry
    {
        return $this->findOneBy([
            'payment' => $payment,
        ]);
    }

    /**
     * Find a registry entry by expense
     */
    public function findOneByExpense(ClassExpense $expense): ?CashStateRegistry
    {
        return $this->findOneBy([
            'expense' => $expense,
        ]);
    }

    /**
     * Find all duplicate registry entries (multiple entries for the same payment or expense)
     *
     * @return array<array{type: string, entity_id: string, count: int}>
     */
    public function findDuplicates(): array
    {
        $conn = $this->getEntityManager()
            ->getConnection();

        // Find duplicate payment entries
        $sql = 'SELECT payment_id, COUNT(*) as count
                FROM classycash.cash_state_registry
                WHERE payment_id IS NOT NULL
                GROUP BY payment_id
                HAVING COUNT(*) > 1';

        $duplicatePayments = $conn->executeQuery($sql)
            ->fetchAllAssociative();

        // Find duplicate expense entries
        $sql = 'SELECT expense_id, COUNT(*) as count
                FROM classycash.cash_state_registry
                WHERE expense_id IS NOT NULL
                GROUP BY expense_id
                HAVING COUNT(*) > 1';

        $duplicateExpenses = $conn->executeQuery($sql)
            ->fetchAllAssociative();

        $duplicates = [];
        foreach ($duplicatePayments as $dup) {
            $duplicates[] = [
                'type' => 'payment',
                'entity_id' => self::normalizeEntityId($dup['payment_id']),
                'count' => self::normalizeCount($dup['count']),
            ];
        }

        foreach ($duplicateExpenses as $dup) {
            $duplicates[] = [
                'type' => 'expense',
                'entity_id' => self::normalizeEntityId($dup['expense_id']),
                'count' => self::normalizeCount($dup['count']),
            ];
        }

        return $duplicates;
    }

    private static function normalizeEntityId(mixed $value): string
    {
        return match (true) {
            is_string($value) => $value,
            is_int($value) => (string) $value,
            default => throw new \UnexpectedValueException(
                sprintf('Unexpected entity id type: %s', get_debug_type($value))
            ),
        };
    }

    private static function normalizeCount(mixed $value): int
    {
        return match (true) {
            is_int($value) => $value,
            is_string($value), is_float($value) => (int) $value,
            default => throw new \UnexpectedValueException(
                sprintf('Unexpected count type: %s', get_debug_type($value))
            ),
        };
    }

    /**
     * Remove duplicate registry entries, keeping only the most recent one for each payment/expense
     *
     * @return int Number of duplicate entries removed
     */
    public function removeDuplicates(): int
    {
        $paymentIds = [];
        $expenseIds = [];
        foreach ($this->findDuplicates() as $duplicate) {
            if ($duplicate['type'] === 'payment') {
                $paymentIds[] = $duplicate['entity_id'];
            } else {
                $expenseIds[] = $duplicate['entity_id'];
            }
        }

        // Load all duplicated entries with one query per type instead of one query per duplicated payment/expense
        /** @var array<string, list<CashStateRegistry>> $groups */
        $groups = [];
        if ($paymentIds !== []) {
            foreach ($this->findBy([
                'payment' => $paymentIds,
            ]) as $entry) {
                $groups['payment:' . $entry->getPayment()?->getId()->toRfc4122()][] = $entry;
            }
        }

        if ($expenseIds !== []) {
            foreach ($this->findBy([
                'expense' => $expenseIds,
            ]) as $entry) {
                $groups['expense:' . $entry->getExpense()?->getId()->toRfc4122()][] = $entry;
            }
        }

        $removedCount = 0;

        foreach ($groups as $entries) {
            // Sort by creation date (newest first)
            usort($entries, fn($a, $b) => $b->getCreatedAt() <=> $a->getCreatedAt());

            // Keep the first (newest) entry, remove the rest
            for ($i = 1; $i < count($entries); $i++) {
                $this->getEntityManager()
                    ->remove($entries[$i]);
                $removedCount++;
            }
        }

        if ($removedCount > 0) {
            $this->getEntityManager()
                ->flush();
        }

        return $removedCount;
    }
}
